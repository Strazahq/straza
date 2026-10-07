package approval

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestVAPIDKeyFileIsNeverReadPartial pins that the VAPID key file appears
// whole or not at all. A reader that polls the path while a boot mints the
// key never reads a file it cannot parse, and two boots that mint at once on
// one shared path both boot on the same key, in every run.
func TestVAPIDKeyFileIsNeverReadPartial(t *testing.T) {
	const runs = 200
	cases := []struct {
		name string
		run  func(path string) error
	}{
		{"a reader polls the file while a boot mints it", func(path string) error {
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				return err
			}
			done := make(chan struct{})
			seen := make(chan error, 1)
			go func() {
				for {
					var finished bool
					select {
					case <-done:
						finished = true
					default:
					}
					raw, err := os.ReadFile(path)
					if err == nil {
						if _, perr := parseVAPIDKeyPEM(raw); perr != nil {
							seen <- fmt.Errorf("the reader saw a file of %d bytes it cannot parse: %w", len(raw), perr)
							return
						}
						seen <- nil
						return
					}
					if finished {
						seen <- fmt.Errorf("the file never appeared: %w", err)
						return
					}
				}
			}()
			err = persistVAPIDKey(path, key)
			close(done)
			if err != nil {
				return err
			}
			return <-seen
		}},
		{"two boots mint at once", func(path string) error {
			keys := make([]*ecdsa.PrivateKey, 2)
			errs := make([]error, 2)
			var wg sync.WaitGroup
			for i := range keys {
				wg.Go(func() { keys[i], _, errs[i] = loadOrMintVAPIDKey(path) })
			}
			wg.Wait()
			if err := errors.Join(errs...); err != nil {
				return err
			}
			if !keys[0].Equal(keys[1]) {
				return errors.New("the two boots hold different keys")
			}
			return nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for i := range runs {
				if err := tc.run(filepath.Join(dir, fmt.Sprintf("vapid-%d.pem", i))); err != nil {
					t.Fatalf("run %d of %d: %v", i+1, runs, err)
				}
			}
		})
	}
}
