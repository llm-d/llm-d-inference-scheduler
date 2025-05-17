package tokenizers

// TODO packaging: how do we build the rust lib for distribution?

/*
#cgo LDFLAGS: -ltokenizers -ldl -lm -lstdc++
#include <stdlib.h>
#include "tokenizers.h"
*/
import "C"

// NOTE: There should be NO space between the comments and the `import "C"` line.
import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"
)

const (
	WANT_VERSION = "1.20.2"

	baseURL = "https://huggingface.co"
)

// List of necessary tokenizer files and their mandatory status.
// True means mandatory, false means optional.
var tokenizerFiles = map[string]bool{
	"tokenizer.json":          true,
	"vocab.txt":               false,
	"merges.txt":              false,
	"special_tokens_map.json": false,
	"added_tokens.json":       false,
}

func init() {
	version := C.version()
	got := C.GoString(version)
	if got != WANT_VERSION {
		panic(fmt.Errorf("tokenizers library version mismatch, want: %s, got: %s", WANT_VERSION, got))
	}
}

type Tokenizer struct {
	tokenizer unsafe.Pointer
}

type tokenizerOpts struct {
	encodeSpecialTokens C.bool
}

type TokenizerOption func(to *tokenizerOpts)

func WithEncodeSpecialTokens() TokenizerOption {
	return func(to *tokenizerOpts) {
		to.encodeSpecialTokens = C.bool(true)
	}
}

type TruncationDirection int

const (
	TruncationDirectionLeft TruncationDirection = iota
	TruncationDirectionRight
)

var _ io.Closer = (*Tokenizer)(nil)

func FromBytes(data []byte, opts ...TokenizerOption) (*Tokenizer, error) {
	allOpts := &tokenizerOpts{
		// by default, we do not encode special tokens
		encodeSpecialTokens: C.bool(false),
	}
	for _, opt := range opts {
		opt(allOpts)
	}
	tokenizer := C.from_bytes((*C.uchar)(unsafe.Pointer(&data[0])), C.uint(len(data)), (*C.struct_TokenizerOptions)(unsafe.Pointer(allOpts)))
	return &Tokenizer{tokenizer: tokenizer}, nil
}

func FromBytesWithTruncation(data []byte, maxLen uint32, dir TruncationDirection) (*Tokenizer, error) {
	tokenizer := C.from_bytes_with_truncation((*C.uchar)(unsafe.Pointer(&data[0])), C.uint(len(data)), C.uint(maxLen), C.uchar(dir))
	return &Tokenizer{tokenizer: tokenizer}, nil
}

func FromFile(path string) (*Tokenizer, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	tokenizer, err := C.from_file(cPath)
	if err != nil {
		return nil, err
	}
	return &Tokenizer{tokenizer: tokenizer}, nil
}

type tokenizerConfig struct {
	cacheDir  *string
	authToken *string
}

type TokenizerConfigOption func(cfg *tokenizerConfig)

func WithCacheDir(path string) TokenizerConfigOption {
	return func(cfg *tokenizerConfig) {
		cfg.cacheDir = &path
	}
}

func WithAuthToken(token string) TokenizerConfigOption {
	return func(cfg *tokenizerConfig) {
		cfg.authToken = &token
	}
}

// FromPretrained downloads necessary files and initializes the tokenizer.
// Parameters:
//   - modelID: The Hugging Face model identifier (e.g., "bert-base-uncased").
//   - destination: Optional. If provided and not nil, files will be downloaded to this folder.
//     If nil, a temporary directory will be used.
//   - authToken: Optional. If provided and not nil, it will be used to authenticate requests.
func FromPretrained(modelID string, opts ...TokenizerConfigOption) (*Tokenizer, error) {
	cfg := &tokenizerConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	if strings.TrimSpace(modelID) == "" {
		return nil, fmt.Errorf("modelID cannot be empty")
	}

	// Construct the model URL
	modelURL := fmt.Sprintf("%s/%s/resolve/main", baseURL, modelID)

	// Determine the download directory
	var downloadDir string
	if cfg.cacheDir != nil {
		downloadDir = fmt.Sprintf("%s/%s", *cfg.cacheDir, modelID)
		// Create the destination directory if it doesn't exist
		err := os.MkdirAll(downloadDir, os.ModePerm)
		if err != nil {
			return nil, fmt.Errorf("failed to create destination directory %s: %w", downloadDir, err)
		}
	} else {
		// Create a temporary directory
		tmpDir, err := os.MkdirTemp("", "huggingface-tokenizer-*")
		if err != nil {
			return nil, fmt.Errorf("error creating temporary directory: %w", err)
		}
		downloadDir = tmpDir
	}

	var wg sync.WaitGroup
	errCh := make(chan error)

	// Download each tokenizer file concurrently
	for filename, isMandatory := range tokenizerFiles {
		wg.Add(1)
		go func(fn string, mandatory bool) {
			defer wg.Done()
			fileURL := fmt.Sprintf("%s/%s", modelURL, fn)
			destPath := filepath.Join(downloadDir, fn)
			err := downloadFile(fileURL, destPath, cfg.authToken)
			if err != nil && mandatory {
				// If the file is mandatory, report an error
				errCh <- fmt.Errorf("failed to download mandatory file %s: %w", fn, err)
			}
		}(filename, isMandatory)
	}

	go func() {
		wg.Wait()
		close(errCh)
	}()

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		if err := os.RemoveAll(downloadDir); err != nil {
			fmt.Printf("Warning: failed to clean up directory %s: %v\n", downloadDir, err)
		}
		return nil, errs[0]
	}

	return FromFile(filepath.Join(downloadDir, "tokenizer.json"))
}

// downloadFile downloads a file from the given URL and saves it to the specified destination.
// If authToken is provided (non-nil), it will be used for authorization.
// Returns an error if the download fails.
func downloadFile(url, destination string, authToken *string) error {
	// Check if the file already exists
	if _, err := os.Stat(destination); err == nil {
		return nil
	}

	// Create a new HTTP request
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request for %s: %w", url, err)
	}

	// If authToken is provided, set the Authorization header
	if authToken != nil {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", *authToken))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download from %s: %w", url, err)
	}
	defer resp.Body.Close()

	// Check for successful response
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download from %s: status code %d", url, resp.StatusCode)
	}

	// Create the destination file
	out, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", destination, err)
	}
	defer out.Close()

	// Write the response body to the file
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write to file %s: %w", destination, err)
	}

	fmt.Printf("Successfully downloaded %s\n", destination)
	return nil
}

func (t *Tokenizer) Close() error {
	C.free_tokenizer(t.tokenizer)
	t.tokenizer = nil
	return nil
}

type Offset [2]uint

type Encoding struct {
	IDs               []uint32
	TypeIDs           []uint32
	SpecialTokensMask []uint32
	AttentionMask     []uint32
	Tokens            []string
	Offsets           []Offset
}

type encodeOpts struct {
	AddSpecialTokens C.bool

	ReturnTypeIDs           C.bool
	ReturnTokens            C.bool
	ReturnSpecialTokensMask C.bool
	ReturnAttentionMask     C.bool
	ReturnOffsets           C.bool
}

type EncodeOption func(eo *encodeOpts)

func uintVecToSlice(arrPtr *C.uint, len int) []uint32 {
	arr := unsafe.Slice(arrPtr, len)
	slice := make([]uint32, len)
	for i, v := range arr {
		slice[i] = uint32(v)
	}
	return slice
}

func offsetVecToSlice(arrPtr *C.size_t, tokenLength int) []Offset {
	arr := unsafe.Slice(arrPtr, tokenLength*2)
	slice := make([]Offset, tokenLength)
	counter := 0
	for i := 0; i < tokenLength; i++ {
		offset := Offset{uint(arr[counter]), uint(arr[counter+1])}
		slice[i] = offset
		counter = counter + 2
	}
	return slice
}

func (t *Tokenizer) Encode(str string, addSpecialTokens bool) ([]uint32, []string) {
	cStr := C.CString(str)
	defer C.free(unsafe.Pointer(cStr))
	options := encodeOpts{
		AddSpecialTokens: C.bool(addSpecialTokens),
		ReturnTokens:     C.bool(true),
	}
	res := C.encode(t.tokenizer, cStr, (*C.struct_EncodeOptions)(unsafe.Pointer(&options)))
	len := int(res.len)
	if len == 0 {
		return nil, nil
	}
	defer C.free_buffer(res)

	ids := uintVecToSlice(res.ids, len)

	var tokens []string
	if res.tokens != nil {
		tokens = make([]string, len)
		for i, s := range (*[1 << 30]*C.char)(unsafe.Pointer(res.tokens))[:len:len] {
			tokens[i] = C.GoString(s)
		}
	}
	return ids, tokens
}

func WithReturnAllAttributes() EncodeOption {
	return func(eo *encodeOpts) {
		eo.ReturnTypeIDs = C.bool(true)
		eo.ReturnSpecialTokensMask = C.bool(true)
		eo.ReturnAttentionMask = C.bool(true)
		eo.ReturnTokens = C.bool(true)
		eo.ReturnOffsets = C.bool(true)
	}
}

func WithReturnTypeIDs() EncodeOption {
	return func(eo *encodeOpts) {
		eo.ReturnTypeIDs = C.bool(true)
	}
}

func WithReturnSpecialTokensMask() EncodeOption {
	return func(eo *encodeOpts) {
		eo.ReturnSpecialTokensMask = C.bool(true)
	}
}

func WithReturnTokens() EncodeOption {
	return func(eo *encodeOpts) {
		eo.ReturnTokens = C.bool(true)
	}
}

func WithReturnAttentionMask() EncodeOption {
	return func(eo *encodeOpts) {
		eo.ReturnAttentionMask = C.bool(true)
	}
}

func WithReturnOffsets() EncodeOption {
	return func(eo *encodeOpts) {
		eo.ReturnOffsets = C.bool(true)
	}
}

func (t *Tokenizer) EncodeWithOptions(str string, addSpecialTokens bool, opts ...EncodeOption) Encoding {
	cStr := C.CString(str)
	defer C.free(unsafe.Pointer(cStr))

	encOptions := encodeOpts{
		AddSpecialTokens: C.bool(addSpecialTokens),
	}
	for _, opt := range opts {
		opt(&encOptions)
	}

	res := C.encode(t.tokenizer, cStr, (*C.struct_EncodeOptions)(unsafe.Pointer(&encOptions)))
	len := int(res.len)
	if len == 0 {
		return Encoding{}
	}
	defer C.free_buffer(res)

	encoding := Encoding{}
	encoding.IDs = uintVecToSlice(res.ids, len)

	if encOptions.ReturnTypeIDs && res.type_ids != nil {
		encoding.TypeIDs = uintVecToSlice(res.type_ids, len)
	}

	if encOptions.ReturnTokens && res.tokens != nil {
		tokens := make([]string, len)
		for i, s := range (*[1 << 30]*C.char)(unsafe.Pointer(res.tokens))[:len:len] {
			tokens[i] = C.GoString(s)
		}
		encoding.Tokens = tokens
	}

	if encOptions.ReturnSpecialTokensMask && res.special_tokens_mask != nil {
		encoding.SpecialTokensMask = uintVecToSlice(res.special_tokens_mask, len)
	}

	if encOptions.ReturnAttentionMask && res.attention_mask != nil {
		encoding.AttentionMask = uintVecToSlice(res.attention_mask, len)
	}

	if encOptions.ReturnOffsets && res.offsets != nil {
		encoding.Offsets = offsetVecToSlice(res.offsets, len)
	}

	return encoding
}

func (t *Tokenizer) Decode(tokenIDs []uint32, skipSpecialTokens bool) string {
	if len(tokenIDs) == 0 {
		return ""
	}
	len := C.uint(len(tokenIDs))
	res := C.decode(t.tokenizer, (*C.uint)(unsafe.Pointer(&tokenIDs[0])), len, C.bool(skipSpecialTokens))
	defer C.free_string(res)
	return C.GoString(res)
}

func (t *Tokenizer) VocabSize() uint32 {
	return uint32(C.vocab_size(t.tokenizer))
}
