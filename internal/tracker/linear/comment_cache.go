package linear

import (
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/domain"
)

// commentPageTTL bounds how long a cached comment page is reused (CORE-165),
// and so how long an edit or deletion inside it can go unseen.
const commentPageTTL = 5 * time.Minute

// maxCachedCommentPages caps the cache; when full, expired pages are dropped
// and, if that is not enough, the cache is cleared (it is only a cost saver).
const maxCachedCommentPages = 512

type commentPage struct {
	comments   []domain.Comment
	hasNext    bool
	nextCursor string
	storedAt   time.Time
}

// commentPageCache holds full middle pages of ascending comment threads,
// keyed by (issue, the cursor the page was read after). Zero value is ready.
type commentPageCache struct {
	mu    sync.Mutex
	pages map[string]commentPage
}

func commentPageKey(issueID, cursor string) string { return issueID + "\x00" + cursor }

func (c *commentPageCache) get(issueID, cursor string, now time.Time) (commentPage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.pages[commentPageKey(issueID, cursor)]
	if !ok || now.Sub(p.storedAt) > commentPageTTL {
		return commentPage{}, false
	}
	return p, true
}

func (c *commentPageCache) put(issueID, cursor string, p commentPage, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pages == nil {
		c.pages = make(map[string]commentPage)
	}
	if len(c.pages) >= maxCachedCommentPages {
		for k, v := range c.pages {
			if now.Sub(v.storedAt) > commentPageTTL {
				delete(c.pages, k)
			}
		}
		if len(c.pages) >= maxCachedCommentPages {
			c.pages = make(map[string]commentPage)
		}
	}
	p.storedAt = now
	c.pages[commentPageKey(issueID, cursor)] = p
}

// commentsAscending reports whether next continues prev in non-decreasing
// CreatedAt order and at least one strict increase has been seen across
// both, i.e. the thread is observably served ascending. A comment with no
// time makes the order undetermined (false).
func commentsAscending(prev, next []domain.Comment) bool {
	all := make([]domain.Comment, 0, len(prev)+len(next))
	all = append(all, prev...)
	all = append(all, next...)
	increased := false
	for i := range all {
		if all[i].CreatedAt == nil {
			return false
		}
		if i == 0 {
			continue
		}
		switch all[i].CreatedAt.Compare(*all[i-1].CreatedAt) {
		case -1:
			return false
		case 1:
			increased = true
		}
	}
	return increased
}
