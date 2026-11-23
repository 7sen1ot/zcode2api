package protocol

import (
	"net/url"
	"strings"
	"unicode/utf8"
	"zcode2api/internal/server/fault"
	"zcode2api/internal/server/shared"
)

const (
	webSearchName       = "web_search"
	nativeWebSearchType = "web_search_20260209"
	NativeWebSearchBeta = "code-execution-web-tools-2026-02-09"
)

func isWebSearch(kind string) bool {
	return kind == webSearchName || kind == "web_search_2025_08_26"
}

func NeedsWebSearchBeta(request shared.M, protocol string) bool {
	for _, value := range shared.Arr(request["tools"]) {
		if shared.Obj(value)["type"] == nativeWebSearchType {
			return true
		}
	}
	if protocol == "responses" {
		for _, message := range shared.Arr(request["messages"]) {
			for _, value := range shared.Arr(shared.Obj(message)["content"]) {
				block := shared.Obj(value)
				if block["type"] == "web_search_tool_result" || block["type"] == "server_tool_use" && block["name"] == webSearchName {
					return true
				}
			}
		}
	}
	return false
}

func convertWebSearch(tool shared.M) (shared.M, error) {
	if access := tool["external_web_access"]; access != nil {
		live, ok := access.(bool)
		if !ok {
			return nil, fault.Invalid("web_search.external_web_access 必须为布尔值")
		}
		if !live {
			return nil, fault.Invalid(`当前 ZCode 搜索转换仅支持实时联网，无法满足只查缓存的要求。可用 codex --search 开启实时搜索，或将 config.toml 中的 web_search 设为 "disabled"。`)
		}
	}
	if access := tool["indexed_web_access"]; access != nil {
		indexed, ok := access.(bool)
		if !ok {
			return nil, fault.Invalid("web_search.indexed_web_access 必须为布尔值")
		}
		if indexed {
			return nil, fault.Invalid("ZCode 上游不支持仅访问索引网页的 web_search，请选择 live 或 disabled 模式")
		}
	}
	if content := tool["search_content_types"]; content != nil {
		types := shared.Arr(content)
		if len(types) != 1 || types[0] != "text" {
			return nil, fault.Invalid("ZCode 上游的 web_search 仅支持文本搜索")
		}
	}
	upstream := shared.M{"type": nativeWebSearchType, "name": webSearchName, "max_uses": 8}
	for field, value := range tool {
		if value == nil {
			continue
		}
		switch field {
		case "type", "external_web_access", "indexed_web_access", "search_content_types", "cache_control":
		case "user_location":
			location, ok := value.(map[string]any)
			if kind, explicit := location["type"]; !ok || explicit && kind != "approximate" {
				return nil, fault.Invalid("web_search.user_location 必须是 approximate 类型的位置对象")
			}
			upstream[field] = shared.Merge(location, shared.M{"type": "approximate"})
		case "filters":
			filters, ok := value.(map[string]any)
			if !ok {
				return nil, fault.Invalid("web_search.filters 必须为对象")
			}
			for key, domains := range filters {
				if key != "allowed_domains" && key != "blocked_domains" {
					return nil, fault.Invalid("ZCode 上游不支持 web_search.filters." + key)
				}
				if domains == nil {
					continue
				}
				list, ok := domains.([]any)
				if !ok || len(list) == 0 {
					return nil, fault.Invalid("web_search.filters." + key + " 必须为非空域名数组")
				}
				for _, value := range list {
					if domain, ok := value.(string); !ok || strings.TrimSpace(domain) == "" {
						return nil, fault.Invalid("web_search.filters." + key + " 必须包含非空域名")
					}
				}
				upstream[key] = list
			}
		default:
			return nil, fault.Invalid("ZCode 上游不支持 web_search." + field)
		}
	}
	if upstream["allowed_domains"] != nil && upstream["blocked_domains"] != nil {
		return nil, fault.Invalid("ZCode 上游的 web_search 只能指定 allowed_domains 或 blocked_domains 其中一项")
	}
	return upstream, nil
}

func webSearchURL(value any) string {
	raw, _ := value.(string)
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return raw
}

func webSearchAction(query string, result shared.M, includeSources bool) shared.M {
	action := shared.M{"type": "search", "query": query}
	if !includeSources {
		return action
	}
	sources := []any{}
	for _, link := range webSearchURLs(shared.Arr(result["content"]), "web_search_result") {
		sources = append(sources, shared.M{"type": "url", "url": link})
	}
	action["sources"] = sources
	return action
}

func webSearchItem(id, status string, action shared.M) shared.M {
	item := shared.M{"id": id, "type": "web_search_call", "status": status}
	if action != nil {
		item["action"] = action
	}
	return item
}

func webSearchStatus(result shared.M) string {
	if result == nil {
		return "incomplete"
	}
	if _, ok := result["content"].([]any); ok {
		return "completed"
	}
	return "failed"
}

func webSearchAnnotations(citations any, text string) []any {
	annotations := []any{}
	entries := shared.Arr(citations)
	if text == "" || len(entries) == 0 {
		return annotations
	}
	end := utf8.RuneCountInString(text)
	for _, value := range entries {
		citation := shared.Obj(value)
		if citation["type"] != "web_search_result_location" {
			continue
		}
		if link := webSearchURL(citation["url"]); link != "" {
			title := shared.Str(shared.Fallback(citation["title"], link))
			annotations = append(annotations, shared.M{"type": "url_citation", "url": link, "title": title, "start_index": 0, "end_index": end})
		}
	}
	return annotations
}

func webSearchHistory(item shared.M) string {
	action := shared.Obj(item["action"])
	label := "Previous web search"
	var details []string
	switch shared.Str(action["type"]) {
	case "open_page", "find_in_page":
		label = "Previous page visit"
		if action["type"] == "find_in_page" {
			label = "Previous in-page search"
			if pattern, ok := action["pattern"].(string); ok && pattern != "" {
				details = append(details, pattern)
			}
		}
		if link := webSearchURL(action["url"]); link != "" {
			details = append(details, link)
		}
	default:
		for _, value := range shared.Arr(action["queries"]) {
			if query, ok := value.(string); ok && query != "" {
				details = append(details, query)
			}
		}
		if query, ok := action["query"].(string); ok && query != "" && len(details) == 0 {
			details = append(details, query)
		}
	}
	switch shared.Str(item["status"]) {
	case "", "completed":
	case "failed":
		label += " failed"
	case "incomplete":
		label += " was incomplete"
	default:
		return ""
	}
	if len(details) > 0 {
		label += ": " + strings.Join(details, ", ")
	}
	if links := webSearchLinks(shared.Arr(action["sources"]), "url"); links != "" {
		label += "\n" + links
	}
	return label
}

func webSearchLinks(entries []any, kind string) string {
	links := webSearchURLs(entries, kind)
	if len(links) == 0 {
		return ""
	}
	return "Sources:\n" + strings.Join(links, "\n")
}

func webSearchURLs(entries []any, kind string) []string {
	var links []string
	seen := map[string]bool{}
	for _, value := range entries {
		entry := shared.Obj(value)
		if entry["type"] == kind {
			if link := webSearchURL(entry["url"]); link != "" && !seen[link] {
				seen[link] = true
				links = append(links, link)
			}
		}
	}
	return links
}
