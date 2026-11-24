package server

import (
	"fmt"
	"strconv"
	"testing"
)

// BenchmarkPushHubFanout measures what one message on a user's subject costs
// a server that holds n push streams, each registered as a daemon's stream is:
// its session, its user and the policy broadcast. Every message names another
// user, as the nudges of a membership sync do.
func BenchmarkPushHubFanout(b *testing.B) {
	for _, n := range []int{1000, 10000, 65536} {
		b.Run(fmt.Sprintf("streams=%d", n), func(b *testing.B) {
			h := newPushHub()
			users := make([]string, n)
			for i := range n {
				id := strconv.Itoa(i)
				users[i] = "straza.push.user.u" + id
				events := map[string]string{"straza.push.session.s" + id: pushEventRevocation, users[i]: pushEventRevocation, policyPushSubject: pushEventPolicy}
				if _, _, err := h.register(events, 0); err != nil {
					b.Fatal(err)
				}
			}
			data := []byte(`{"kind":"user","id":"u"}`)
			b.ResetTimer()
			for i := range b.N {
				h.fanout(users[i%n], data)
			}
		})
	}
}
