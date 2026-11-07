"""Compile and validate complete tool inputs using LLGuidance's actual grammar dialect."""
import json
import sys


def main():
    try:
        from llguidance import LLMatcher, LLParserLimits, LLTokenizer, TokenizerWrapper
    except ImportError:
        return {"ok": False, "code": "grammar_runtime_unavailable"}

    if sys.platform == "linux":
        import resource
        _, maximum = resource.getrlimit(resource.RLIMIT_AS)
        limit = 512 << 20
        if maximum != resource.RLIM_INFINITY:
            limit = min(limit, maximum)
        resource.setrlimit(resource.RLIMIT_AS, (limit, limit))

    request = json.loads(sys.stdin.buffer.read())
    syntax, definition = request["syntax"], request["definition"]
    if syntax == "lark":
        grammar = LLMatcher.grammar_from_lark(definition)
    elif syntax == "regex":
        grammar = LLMatcher.grammar_from_regex(definition)
    else:
        return {"ok": False, "code": "invalid_grammar", "message": "Unsupported grammar syntax"}

    # One token per byte makes matching independent of any model tokenizer.
    class ByteTokenizer:
        eos_token_id = 256
        bos_token_id = None
        special_token_ids = [256]
        tokens = [bytes([value]) for value in range(256)] + [b"<|endoftext|>"]

        def __call__(self, value):
            return list(value if isinstance(value, bytes) else value.encode("utf-8"))

    tokenizer = LLTokenizer(TokenizerWrapper(ByteTokenizer()), slices=[])
    limits = LLParserLimits(max_grammar_size=200000, max_items_in_row=2000)
    failed, messages = LLMatcher.validate_grammar_with_warnings(grammar, tokenizer, limits=limits)
    if failed or messages:
        # A warning can describe a relaxed constraint; do not silently accept it.
        return {"ok": False, "code": "invalid_grammar", "message": "; ".join(messages)[:512]}
    if "input" not in request:
        return {"ok": True}

    matcher = LLMatcher(tokenizer, grammar, limits=limits, log_level=0)
    accepted = matcher.consume_tokens(list(request["input"].encode("utf-8")))
    if not accepted or matcher.is_error() or not matcher.is_accepting():
        return {"ok": False, "code": "grammar_mismatch"}
    return {"ok": True}


try:
    result = main()
except Exception as error:
    result = {"ok": False, "code": "invalid_grammar", "message": str(error)[:512]}
sys.stdout.write(json.dumps(result))
