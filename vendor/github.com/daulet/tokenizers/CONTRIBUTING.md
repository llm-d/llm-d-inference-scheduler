
# Capture benchmarks

Run the following command to capture benchmarks for your current commit:
```
make build
go test . -bench=. -benchmem -count=6 -timeout 30m | tee benchmarks/$(git rev-parse HEAD).txt
```

Then do the same for the previous commit in upstream/main and then publish the diff along with your PR:
```
git checkout .
benchstat benchmarks/$(git rev-parse HEAD^1).txt benchmarks/$(git rev-parse HEAD).txt
```

It should look something like this:
```
goos: darwin
goarch: arm64
pkg: github.com/daulet/tokenizers
                 │ benchmarks/786da4095f5ca3d598db1236c46401b63874f640.txt │ benchmarks/38a9a14c1c56b113461b0c7350c72de949e23cc2.txt │
                 │                         sec/op                          │              sec/op                vs base              │
EncodeNTimes-10                                              13.26µ ±   4%                       13.11µ ±   1%  -1.09% (p=0.041 n=6)
EncodeNChars-10                                              3.170n ± 530%                       2.989n ± 272%       ~ (p=0.937 n=6)
DecodeNTimes-10                                              4.496µ ±   4%                       4.535µ ±   2%       ~ (p=0.132 n=6)
DecodeNTokens-10                                             646.8n ±   6%                       656.1n ±   3%       ~ (p=0.589 n=6)
geomean                                                      591.2n                              584.3n         -1.17%

                 │ benchmarks/786da4095f5ca3d598db1236c46401b63874f640.txt │ benchmarks/38a9a14c1c56b113461b0c7350c72de949e23cc2.txt │
                 │                          B/op                           │              B/op                vs base                │
EncodeNTimes-10                                               232.0 ± 0%                          232.0 ± 0%       ~ (p=1.000 n=6) ¹
EncodeNChars-10                                               0.000 ± 0%                          0.000 ± 0%       ~ (p=1.000 n=6) ¹
DecodeNTimes-10                                               96.00 ± 0%                          96.00 ± 0%       ~ (p=1.000 n=6) ¹
DecodeNTokens-10                                              7.000 ± 0%                          7.000 ± 0%       ~ (p=1.000 n=6) ¹
geomean                                                                  ²                                    +0.00%               ²
¹ all samples are equal
² summaries must be >0 to compute geomean

                 │ benchmarks/786da4095f5ca3d598db1236c46401b63874f640.txt │ benchmarks/38a9a14c1c56b113461b0c7350c72de949e23cc2.txt │
                 │                        allocs/op                        │            allocs/op             vs base                │
EncodeNTimes-10                                               12.00 ± 0%                          12.00 ± 0%       ~ (p=1.000 n=6) ¹
EncodeNChars-10                                               0.000 ± 0%                          0.000 ± 0%       ~ (p=1.000 n=6) ¹
DecodeNTimes-10                                               3.000 ± 0%                          3.000 ± 0%       ~ (p=1.000 n=6) ¹
DecodeNTokens-10                                              0.000 ± 0%                          0.000 ± 0%       ~ (p=1.000 n=6) ¹
geomean                                                                  ²                                    +0.00%               ²
¹ all samples are equal
² summaries must be >0 to compute geomean
```
