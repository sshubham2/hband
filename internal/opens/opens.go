// Package opens counts how many times each report was opened: one open per
// visitor, per report, per UTC day.
//
// It is the only code that names the report_opens table in SQL. Every write is
// one SQL statement and this package never opens a transaction: a transaction
// that reads first and writes second is upgraded from a read snapshot, and under
// WAL SQLite refuses that upgrade at once, without waiting on busy_timeout,
// whenever another connection has written in between. Record runs on every
// public report page, so a single statement that waits its turn is the only
// shape that does not lose opens under load.
//
// A visitor is never stored. Record keeps the first 16 bytes of an HMAC-SHA256
// over the report, the UTC day, the address family and the address (the whole of
// an IPv4 address, or the /64 of an IPv6 one), keyed by 32 random bytes drawn when
// the process starts and held only in memory. Nothing on disk and nothing in the
// configuration can turn a stored value back into an address, and putting the
// report in the input stops one visitor being followed from report to report. The
// cost is that a restart part-way through a UTC day draws a new key, so a visitor
// who comes back that day after the restart counts once more. That overcount is
// bounded by one open per visitor per report per restart, and is accepted.
//
// This package never logs or returns an address.
package opens

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

const secondsPerDay = 24 * 60 * 60

// errNoAddress is returned by Record for an address that is not an address.
var errNoAddress = errors.New("opens: no visitor address")

// Book is the opens of one database.
type Book struct {
	db  *sql.DB
	now func() time.Time
	key []byte
}

// New returns the book over db, which must have foreign keys enforced (the
// store's serving pool does) so that deleting a report deletes its opens. It
// draws the key that makes visitors unrecoverable; it panics only if the
// operating system has no randomness, as crypto/rand itself does.
func New(db *sql.DB, now func() time.Time) *Book {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(fmt.Sprintf("opens: draw the visitor key: %v", err))
	}
	return newBook(db, now, key)
}

// newBook is New with the key given, so a test can hold it and know what a
// visitor looks like.
func newBook(db *sql.DB, now func() time.Time, key []byte) *Book {
	return &Book{db: db, now: now, key: key}
}

// visitor is the stored identity of addr on a report and a UTC day.
func (b *Book) visitor(reportID, day int64, addr netip.Addr) ([]byte, error) {
	addr = addr.Unmap().WithZone("")
	var family byte
	var raw []byte
	switch {
	case addr.Is4():
		a := addr.As4()
		family, raw = 4, a[:]
	case addr.Is6():
		// The /64: one host can take any address of its own network, so the network
		// is the visitor.
		a := addr.As16()
		family, raw = 6, a[:8]
	default:
		return nil, errNoAddress
	}
	mac := hmac.New(sha256.New, b.key)
	var num [8]byte
	binary.BigEndian.PutUint64(num[:], uint64(reportID))
	mac.Write(num[:])
	binary.BigEndian.PutUint64(num[:], uint64(day))
	mac.Write(num[:])
	mac.Write([]byte{family})
	mac.Write(raw)
	return mac.Sum(nil)[:16], nil
}

// Record counts an open of the report by the visitor at addr, once for that
// visitor on this UTC day. It is one statement: the insert is the dedupe and the
// count at once. The report's published status is checked inside that statement,
// so an open can never attach to a draft, or to a report that was unpublished or
// deleted after the caller looked it up. A repeat, a draft and an unknown id write
// nothing and return nil. An error never carries the address.
func (b *Book) Record(ctx context.Context, reportID int64, addr netip.Addr) error {
	day := b.now().UTC().Unix() / secondsPerDay
	v, err := b.visitor(reportID, day, addr)
	if err != nil {
		return fmt.Errorf("opens: record: %w", err)
	}
	_, err = b.db.ExecContext(ctx, `
INSERT INTO report_opens (report_id, day, visitor)
SELECT id, ?, ? FROM reports WHERE id = ? AND status = 'published'
ON CONFLICT (report_id, day, visitor) DO NOTHING`, day, v, reportID)
	if err != nil {
		return fmt.Errorf("opens: record: %w", err)
	}
	return nil
}

// Counts returns the times opened of every report that has an open, in one read.
// A report with none is absent from the map, which reads as 0. An error is
// returned for a query or scan failure and never a partial map.
func (b *Book) Counts(ctx context.Context) (map[int64]int, error) {
	rows, err := b.db.QueryContext(ctx, `SELECT report_id, COUNT(*) FROM report_opens GROUP BY report_id`)
	if err != nil {
		return nil, fmt.Errorf("opens: count: %w", err)
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("opens: count: %w", err)
		}
		out[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("opens: count: %w", err)
	}
	return out, nil
}

// botTokens are the pieces of a User-Agent that mark a request as a program: a
// crawler, a link-preview fetcher, a headless browser, a command-line client or a
// library's default. Matching is case-insensitive. The list is the whole of the
// "obvious bots" rule, and the one place to add to it.
var botTokens = []string{
	"bot", "crawl", "spider", "slurp", "preview", "headless", "lighthouse",
	"facebookexternalhit", "curl", "wget", "python", "go-http", "java/", "okhttp", "scrapy",
}

// ObviousBot reports whether a request's User-Agent is one that must not count as
// a reader: it is empty, it does not start a browser's way (every mainstream
// browser's carries "Mozilla/"), or it holds a bot token. Googlebot and bingbot
// carry "Mozilla/", so the tokens are what catch them. "Cubot" is a make of phone
// whose Chrome says so in its User-Agent: it holds "bot" and is a reader, so it is
// taken out before the tokens are matched.
func ObviousBot(ua string) bool {
	ua = strings.TrimSpace(ua)
	if ua == "" || !strings.Contains(ua, "Mozilla/") {
		return true
	}
	lower := strings.ReplaceAll(strings.ToLower(ua), "cubot", "")
	for _, t := range botTokens {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}
