package provider

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

// IncreaseSOASerial returns the serial to write with a change of a zone whose
// SOA record has the serial current (see nextSOASerial).
func IncreaseSOASerial(current string) (string, error) {
	return nextSOASerial(current, time.Now())
}

// nextSOASerial returns the serial following current at the time now. It is
// today's first date serial (YYYYMMDD01) or current + 1, whichever is
// numerically greater, so the serial never goes backwards, also after 99
// changes on one day (YYYYMMDD99 is followed by the next day's YYYYMMDD00)
// and for serials ahead of today's date or not date based at all. An empty
// current (no serial yet) yields today's first serial.
//
// Serials are unsigned 32-bit numbers (RFC 1035), which PowerDNS enforces.
// At the maximum, the serial wraps around as RFC 1982 serial number
// arithmetic allows: to today's first serial, which is then greater than
// current in serial arithmetic (until the year 2147), or else to 0.
func nextSOASerial(current string, now time.Time) (string, error) {
	date, err := strconv.ParseUint(now.Format("20060102"), 10, 64)
	if err != nil {
		return "", err
	}
	first := date*100 + 1
	if current == "" {
		return strconv.FormatUint(first, 10), nil
	}

	serial, err := strconv.ParseUint(current, 10, 64)
	if err != nil || serial > math.MaxUint32 {
		return "", fmt.Errorf("the current SOA serial %q is not an unsigned 32-bit number", current)
	}

	switch {
	case first > serial:
		return strconv.FormatUint(first, 10), nil
	case serial < math.MaxUint32:
		return strconv.FormatUint(serial+1, 10), nil
	case serialGreater(first, serial):
		return strconv.FormatUint(first, 10), nil
	default:
		return "0", nil
	}
}

// serialGreater reports whether serial a is greater than b in RFC 1982
// serial number arithmetic (SERIAL_BITS = 32).
func serialGreater(a, b uint64) bool {
	d := (a - b) & math.MaxUint32
	return d != 0 && d < 1<<31
}
