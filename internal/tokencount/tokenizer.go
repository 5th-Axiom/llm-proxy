package tokencount

import (
	"sync"

	"github.com/tiktoken-go/tokenizer"
	"github.com/tiktoken-go/tokenizer/codec"
)

var (
	defaultCodec tokenizer.Codec
	codecCache   = make(map[string]tokenizer.Codec)
	codecMu      sync.RWMutex
	initialized  bool
)

// Init loads the default BPE codec. Call once at startup.
func Init() error {
	defaultCodec = codec.NewCl100kBase()
	initialized = true
	return nil
}

// countTokensAccurate uses tiktoken BPE for accurate token counting.
// Returns -1 if the tokenizer was not initialized.
func countTokensAccurate(model string, text string) int {
	if !initialized || text == "" {
		if !initialized {
			return -1
		}
		return 0
	}

	enc := getCodec(model)
	n, _ := enc.Count(text)
	return n
}

func getCodec(model string) tokenizer.Codec {
	codecMu.RLock()
	if c, ok := codecCache[model]; ok {
		codecMu.RUnlock()
		return c
	}
	codecMu.RUnlock()

	codecMu.Lock()
	defer codecMu.Unlock()

	if c, ok := codecCache[model]; ok {
		return c
	}

	c, err := tokenizer.ForModel(tokenizer.Model(model))
	if err != nil {
		codecCache[model] = defaultCodec
		return defaultCodec
	}
	codecCache[model] = c
	return c
}
