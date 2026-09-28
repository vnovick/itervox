package linear

import (
	"testing"
	"time"
)

// TestCommentPageCacheTTLAndCap (CORE-165): a page expires after
// commentPageTTL, and the cache never exceeds maxCachedCommentPages.
func TestCommentPageCacheTTLAndCap(t *testing.T) {
	var c commentPageCache
	now := time.Now()
	c.put("i", "50", commentPage{hasNext: true, nextCursor: "100"}, now)
	if _, ok := c.get("i", "50", now.Add(commentPageTTL-time.Second)); !ok {
		t.Fatal("page should be served within the TTL")
	}
	if _, ok := c.get("i", "50", now.Add(commentPageTTL+time.Second)); ok {
		t.Fatal("page must expire after the TTL")
	}
	for i := 0; i < maxCachedCommentPages+10; i++ {
		c.put("i", string(rune('a'+i%26))+time.Duration(i).String(), commentPage{}, now)
	}
	if len(c.pages) > maxCachedCommentPages {
		t.Fatalf("cache holds %d pages; cap is %d", len(c.pages), maxCachedCommentPages)
	}
}
