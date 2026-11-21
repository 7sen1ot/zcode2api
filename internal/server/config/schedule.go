package config

import (
	"fmt"
	"strconv"
	"strings"
)

const DefaultDailyClaimTime = "19:02"

func DailyClaimClock(value string) (int, int) {
	hh, mm, found := strings.Cut(strings.TrimSpace(value), ":")
	h, e1 := strconv.Atoi(hh)
	m, e2 := strconv.Atoi(mm)
	if !found || e1 != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 19, 2
	}
	return h, m
}

func validDailyClaimTime(value string) bool {
	h, m := DailyClaimClock(value)
	return fmt.Sprintf("%02d:%02d", h, m) == strings.TrimSpace(value)
}
