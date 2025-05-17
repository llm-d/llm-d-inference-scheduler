from pathlib import Path
import random
import time

import tiktoken
from tiktoken.load import load_tiktoken_bpe
import tokenizers


def bench_tiktoken_llama3():
    model_path = "test/data/Meta-Llama-3-8B-Instruct.model"
    num_reserved_special_tokens = 256
    pat_str = r"(?i:'s|'t|'re|'ve|'m|'ll|'d)|[^\r\n\p{L}\p{N}]?\p{L}+|\p{N}{1,3}| ?[^\s\p{L}\p{N}]+[\r\n]*|\s*[\r\n]+|\s+(?!\S)|\s+"  # noqa: E501
    mergeable_ranks = load_tiktoken_bpe(model_path)
    num_base_tokens = len(mergeable_ranks)
    special_tokens = [
        "<|begin_of_text|>",
        "<|end_of_text|>",
        "<|reserved_special_token_0|>",
        "<|reserved_special_token_1|>",
        "<|reserved_special_token_2|>",
        "<|reserved_special_token_3|>",
        "<|start_header_id|>",
        "<|end_header_id|>",
        "<|reserved_special_token_4|>",
        "<|eot_id|>",  # end of turn
    ] + [
        f"<|reserved_special_token_{i}|>"
        for i in range(5, num_reserved_special_tokens - 5)
    ]
    special_tokens = {
        token: num_base_tokens + i for i, token in enumerate(special_tokens)
    }
    tokenizer = tiktoken.Encoding(
        name=Path(model_path).name,
        pat_str=pat_str,
        mergeable_ranks=mergeable_ranks,
        special_tokens=special_tokens,
    )

    def encode(text):
        return tokenizer.encode(text)
    def decode(tokens):
        return tokenizer.decode(tokens)
    return encode, decode


def bench_tokenizers_llama3():
    tokenizer = tokenizers.Tokenizer.from_file("test/data/Meta-Llama-3-8B-Instruct.json")

    def encode(text):
        return tokenizer.encode(text, add_special_tokens=False).ids
    def decode(tokens):
        return tokenizer.decode(tokens)
    return encode, decode


def bench_encode(encodeFn, text):
    start = time.perf_counter_ns()
    res = encodeFn(text)
    end = time.perf_counter_ns()
    print(f" \t{len(text) / (end - start) * 1e9:.2f} chars / s")
    return res


def bench_decode(decodeFn, tokens):
    start = time.perf_counter_ns()
    res = decodeFn(tokens)
    end = time.perf_counter_ns()
    
    print(f" \t{(end - start)/1e3:.2f} microsec")
    return res


if __name__ == "__main__":
    times = 10
    text = Path("test/data/long_text.txt").read_text()
    # split text into times
    texts = [text[i:i + len(text) // times] for i in range(0, len(text), len(text) // times)]
    
    print("TikToken:")
    enc, dec = bench_tiktoken_llama3()
    token_groups = []
    for i in range(times):
        tokens = bench_encode(enc, texts[i])
        token_groups.append(tokens)
    for i in range(1, 4):
        token_groups.append([random.randint(0, 1000) for _ in range(i)])
    for tokens in token_groups:
        bench_decode(dec, tokens)
    
    print("Tokenizers:")
    enc, dec = bench_tokenizers_llama3()
    for i in range(times):
        tokens = bench_encode(enc, texts[i])
        assert tokens == token_groups[i]
    for tokens in token_groups:
        bench_decode(dec, tokens)
