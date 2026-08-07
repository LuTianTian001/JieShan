package gateway

import (
	"strings"

	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

const (
	meteringPending       = "pending"
	meteringMetered       = "metered"
	meteringUnavailable   = "unavailable"
	meteringNotApplicable = "not_applicable"

	meteringErrorUsageUnavailable = "usage_unavailable"
)

func recordMetering(result *Result, usage protocol.Usage, err error) {
	if err != nil {
		result.Usage = protocol.Usage{}
		result.MeteringStatus = meteringUnavailable
		result.MeteringErrorCode = meteringErrorUsageUnavailable
		return
	}
	result.Usage = usage
	result.MeteringStatus = meteringMetered
	result.MeteringErrorCode = ""
}

func responseModel(reported, configured string) string {
	if reported = strings.TrimSpace(reported); reported != "" {
		return reported
	}
	return strings.TrimSpace(configured)
}
