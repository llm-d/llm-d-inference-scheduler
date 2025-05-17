# Tokenizers

Go bindings for the [HuggingFace Tokenizers](https://github.com/huggingface/tokenizers) library.

## Installation

`make build` to build `libtokenizers.a` that you need to run your application that uses bindings. In addition, you need to inform the linker where to find that static library: `go run -ldflags="-extldflags '-L./path/to/libtokenizers/directory'" .` or just add it to the `CGO_LDFLAGS` environment variable: `CGO_LDFLAGS="-L./path/to/libtokenizers/directory"` to avoid specifying it every time.

### Using pre-built binaries

If you don't want to install Rust toolchain, build it in docker: `docker build --platform=linux/amd64 -f release/Dockerfile .` or use prebuilt binaries from the [releases](https://github.com/daulet/tokenizers/releases) page. Prebuilt libraries are available for:

* [darwin-arm64](https://github.com/daulet/tokenizers/releases/latest/download/libtokenizers.darwin-arm64.tar.gz)
* [linux-arm64](https://github.com/daulet/tokenizers/releases/latest/download/libtokenizers.linux-arm64.tar.gz)
* [linux-amd64](https://github.com/daulet/tokenizers/releases/latest/download/libtokenizers.linux-amd64.tar.gz)

## Getting started

TLDR: [working example](example/main.go).

Load a tokenizer from a JSON config:

```go
import "github.com/daulet/tokenizers"

tk, err := tokenizers.FromFile("./data/bert-base-uncased.json")
if err != nil {
    return err
}
// release native resources
defer tk.Close()
```

Load a tokenizer from Huggingface:

```go
import "github.com/daulet/tokenizers"

tk, err := tokenizers.FromPretrained("google-bert/bert-base-uncased")
if err != nil {
    return err
}
// release native resources
defer tk.Close()
```

Encode text and decode tokens:

```go
fmt.Println("Vocab size:", tk.VocabSize())
// Vocab size: 30522
fmt.Println(tk.Encode("brown fox jumps over the lazy dog", false))
// [2829 4419 14523 2058 1996 13971 3899] [brown fox jumps over the lazy dog]
fmt.Println(tk.Encode("brown fox jumps over the lazy dog", true))
// [101 2829 4419 14523 2058 1996 13971 3899 102] [[CLS] brown fox jumps over the lazy dog [SEP]]
fmt.Println(tk.Decode([]uint32{2829, 4419, 14523, 2058, 1996, 13971, 3899}, true))
// brown fox jumps over the lazy dog
```

Encode text with options:

```go
var encodeOptions []tokenizers.EncodeOption
encodeOptions = append(encodeOptions, tokenizers.WithReturnTypeIDs())
encodeOptions = append(encodeOptions, tokenizers.WithReturnAttentionMask())
encodeOptions = append(encodeOptions, tokenizers.WithReturnTokens())
encodeOptions = append(encodeOptions, tokenizers.WithReturnOffsets())
encodeOptions = append(encodeOptions, tokenizers.WithReturnSpecialTokensMask())

// Or just basically
// encodeOptions = append(encodeOptions, tokenizers.WithReturnAllAttributes())

encodingResponse := tk.EncodeWithOptions("brown fox jumps over the lazy dog", false, encodeOptions...)
fmt.Println(encodingResponse.IDs)
// [2829 4419 14523 2058 1996 13971 3899]
fmt.Println(encodingResponse.TypeIDs)
// [0 0 0 0 0 0 0]
fmt.Println(encodingResponse.SpecialTokensMask)
// [0 0 0 0 0 0 0]
fmt.Println(encodingResponse.AttentionMask)
// [1 1 1 1 1 1 1]
fmt.Println(encodingResponse.Tokens)
// [brown fox jumps over the lazy dog]
fmt.Println(encodingResponse.Offsets)
// [[0 5] [6 9] [10 15] [16 20] [21 24] [25 29] [30 33]]
```

## Benchmarks

`go test . -run=^\$ -bench=. -benchmem -count=10 > test/benchmark/$(git rev-parse HEAD).txt`

Decoding overhead (due to CGO and extra allocations) is between 2% to 9% depending on the benchmark.

```bash
go test . -bench=. -benchmem -benchtime=10s

goos: darwin
goarch: arm64
pkg: github.com/daulet/tokenizers
BenchmarkEncodeNTimes-10     	  959494	     12622 ns/op	     232 B/op	      12 allocs/op
BenchmarkEncodeNChars-10      1000000000	     2.046 ns/op	       0 B/op	       0 allocs/op
BenchmarkDecodeNTimes-10     	 2758072	      4345 ns/op	      96 B/op	       3 allocs/op
BenchmarkDecodeNTokens-10    	18689725	     648.5 ns/op	       7 B/op	       0 allocs/op
PASS
ok   github.com/daulet/tokenizers 126.681s
```

Run equivalent Rust tests with `cargo bench`.

```bash
decode_n_times          time:   [3.9812 µs 3.9874 µs 3.9939 µs]
                        change: [-0.4103% -0.1338% +0.1275%] (p = 0.33 > 0.05)
                        No change in performance detected.
Found 7 outliers among 100 measurements (7.00%)
  7 (7.00%) high mild

decode_n_tokens         time:   [651.72 ns 661.73 ns 675.78 ns]
                        change: [+0.3504% +2.0016% +3.5507%] (p = 0.01 < 0.05)
                        Change within noise threshold.
Found 7 outliers among 100 measurements (7.00%)
  2 (2.00%) high mild
  5 (5.00%) high severe
```

## Contributing

Please refer to [CONTRIBUTING.md](CONTRIBUTING.md) for information on how to contribute a PR to this project.
