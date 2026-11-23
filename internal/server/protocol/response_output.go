package protocol

import "zcode2api/internal/server/shared"

type outputPosition struct {
	id    string
	index int
}

func (p outputPosition) fields(data shared.M) shared.M {
	data["item_id"], data["output_index"] = p.id, p.index
	return data
}

type responseCall struct {
	outputPosition
	callID     string
	definition toolDefinition
}

type responseSearch struct {
	outputPosition
	callID, query string
}

// ResponseOutput assembles an attempt's ordered output. A stream can also
// publish its lifecycle events, using the same item IDs as the final response.
type ResponseOutput struct {
	catalog     *ToolCatalog
	emit        func(string, shared.M)
	items       []any
	content     map[int]outputPosition
	calls       map[int]responseCall
	searches    []*responseSearch
	lastMessage shared.M
}

func NewResponseOutput(tools *ToolCatalog) *ResponseOutput {
	return &ResponseOutput{catalog: tools, items: []any{}, content: map[int]outputPosition{}, calls: map[int]responseCall{}}
}

func (o *ResponseOutput) event(kind string, data shared.M) {
	if o.emit != nil {
		o.emit(kind, data)
	}
}

func (o *ResponseOutput) addItem(item shared.M) outputPosition {
	o.finishLastMessage("completed")
	p := outputPosition{id: shared.Str(item["id"]), index: len(o.items)}
	o.items = append(o.items, nil)
	o.event("response.output_item.added", shared.M{"output_index": p.index, "item": item})
	return p
}

func (o *ResponseOutput) finishItem(index int, item shared.M) {
	o.items[index] = item
	o.event("response.output_item.done", shared.M{"output_index": index, "item": item})
}

// A trailing text block closes before message_stop reveals whether it was truncated.
func (o *ResponseOutput) finishLastMessage(status string) {
	if o.lastMessage != nil {
		o.lastMessage["status"] = status
		o.finishItem(len(o.items)-1, o.lastMessage)
		o.lastMessage = nil
	}
}

func (o *ResponseOutput) OnEvent(e Event) error {
	switch e.Type {
	case "text_delta", "reasoning_delta":
		if e.Text != "" {
			o.contentDelta(e)
		}
	case "content_end":
		o.finishContent(e)
	case "tool_start":
		d := o.catalog.definition(e.Name)
		p := o.addItem(d.startItem(d.responseItemID(), e.ID))
		o.calls[e.Index] = responseCall{outputPosition: p, callID: e.ID, definition: d}
	case "tool_delta":
		if call, ok := o.calls[e.Index]; ok && call.definition.Kind != "custom" && o.emit != nil {
			o.emit("response.function_call_arguments.delta", call.fields(shared.M{"delta": e.Delta}))
		}
	case "tool_end", "tool_incomplete":
		return o.finishCall(e)
	case "web_search_start":
		p := o.addItem(webSearchItem("ws_"+shared.UUID(), "in_progress", nil))
		o.searches = append(o.searches, &responseSearch{outputPosition: p, callID: e.ID})
		o.event("response.web_search_call.in_progress", p.fields(shared.M{}))
	case "web_search_query", "web_search_result":
		for _, search := range o.searches {
			if search.callID == e.ID && o.items[search.index] == nil {
				if e.Type == "web_search_query" {
					search.query = e.Text
					o.event("response.web_search_call.searching", search.fields(shared.M{}))
				} else {
					o.finishSearch(search, e.Block)
				}
				break
			}
		}
	}
	return nil
}

func (o *ResponseOutput) contentDelta(e Event) {
	reasoning := e.Type == "reasoning_delta"
	p, ok := o.content[e.Index]
	if !ok {
		if reasoning {
			p = o.addItem(shared.M{"id": "rs_" + shared.UUID(), "type": "reasoning", "summary": []any{}})
			o.event("response.reasoning_summary_part.added", p.fields(shared.M{"summary_index": 0, "part": shared.M{"type": "summary_text", "text": ""}}))
		} else {
			p = o.addItem(shared.M{"id": "msg_" + shared.UUID(), "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}})
			o.event("response.content_part.added", p.fields(shared.M{"content_index": 0, "part": shared.M{"type": "output_text", "text": "", "annotations": []any{}}}))
		}
		o.content[e.Index] = p
	}
	if o.emit != nil {
		if reasoning {
			o.emit("response.reasoning_summary_text.delta", p.fields(shared.M{"summary_index": 0, "delta": e.Text}))
		} else {
			o.emit("response.output_text.delta", p.fields(shared.M{"content_index": 0, "delta": e.Text, "logprobs": []any{}}))
		}
	}
}

func (o *ResponseOutput) finishContent(e Event) {
	p, ok := o.content[e.Index]
	if !ok {
		return // Empty native blocks have no public output item.
	}
	delete(o.content, e.Index)
	if e.Block["type"] == "thinking" {
		text := shared.Str(e.Block["thinking"])
		part := shared.M{"type": "summary_text", "text": text}
		o.event("response.reasoning_summary_text.done", p.fields(shared.M{"summary_index": 0, "text": text}))
		o.event("response.reasoning_summary_part.done", p.fields(shared.M{"summary_index": 0, "part": part}))
		o.finishItem(p.index, shared.M{"id": p.id, "type": "reasoning", "summary": []any{part}})
		return
	}
	text := shared.Str(e.Block["text"])
	annotations := webSearchAnnotations(e.Block["citations"], text)
	part := shared.M{"type": "output_text", "text": text, "annotations": annotations}
	if o.emit != nil {
		for i, annotation := range annotations {
			o.emit("response.output_text.annotation.added", p.fields(shared.M{"content_index": 0, "annotation_index": i, "annotation": annotation}))
		}
		o.emit("response.output_text.done", p.fields(shared.M{"content_index": 0, "text": text, "logprobs": []any{}}))
		o.emit("response.content_part.done", p.fields(shared.M{"content_index": 0, "part": part}))
	}
	item := shared.M{"id": p.id, "type": "message", "role": "assistant", "status": "completed", "content": []any{part}}
	if p.index == len(o.items)-1 {
		o.lastMessage = item
	} else {
		o.finishItem(p.index, item)
	}
}

func (o *ResponseOutput) finishCall(e Event) error {
	call, ok := o.calls[e.Index]
	if !ok {
		return nil
	}
	delete(o.calls, e.Index)
	d := call.definition
	if e.Type == "tool_incomplete" {
		o.finishItem(call.index, d.incompleteItem(call.id, call.callID, e.Delta))
		return nil
	}
	var arguments any = e.Delta
	if d.Kind == "custom" {
		arguments = e.Block["input"]
	}
	item, err := d.completedItem(call.id, call.callID, arguments)
	if err != nil {
		return err
	}
	if d.Kind == "custom" {
		// The upstream reader has validated the complete input and its grammar.
		o.event("response.custom_tool_call_input.delta", call.fields(shared.M{"delta": item["input"]}))
		o.event("response.custom_tool_call_input.done", call.fields(shared.M{"input": item["input"]}))
	} else {
		o.event("response.function_call_arguments.done", call.fields(shared.M{"arguments": item["arguments"]}))
	}
	o.finishItem(call.index, item)
	return nil
}

func (o *ResponseOutput) finishSearch(search *responseSearch, result shared.M) {
	status := webSearchStatus(result)
	if status == "completed" {
		o.event("response.web_search_call.completed", search.fields(shared.M{}))
	}
	action := webSearchAction(search.query, result, o.catalog.includeSearchSources)
	o.finishItem(search.index, webSearchItem(search.id, status, action))
}

func (o *ResponseOutput) Finish(r *Result) []any {
	o.finishLastMessage(r.outputStatus())
	for _, search := range o.searches {
		if o.items[search.index] == nil {
			o.finishSearch(search, nil)
		}
	}
	return o.items
}

func (o *ResponseOutput) completedItems() []any {
	items := make([]any, 0, len(o.items))
	for _, item := range o.items {
		if item != nil {
			items = append(items, item)
		}
	}
	return items
}
