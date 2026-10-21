package vaultimport

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	detectPuttyRegistry = regexp.MustCompile(`(?i)Windows Registry Editor Version|\\Software\\SimonTatham\\PuTTY\\Sessions`)
	detectMobaBookmark  = regexp.MustCompile(`(?im)^\[Bookmarks(?:_\d+)?\]\s*$`)
	detectMobaSubRep    = regexp.MustCompile(`(?im)^SubRep=.*$`)
	detectMobaImgNum    = regexp.MustCompile(`(?im)^ImgNum=\d+\s*$`)
	detectMobaSession   = regexp.MustCompile(`(?im)^[^=\r\n]+=\s*(?:;\s*logout)?\s*#\d+#\d+%[^%\r\n]+%\d+`)
	detectMobaMisc      = regexp.MustCompile(`(?im)^\[Misc\]\s*$`)
	detectMobaPasswords = regexp.MustCompile(`(?im)^(?:SessionP=|\[Passwords\]\s*$|\[Credentials\]\s*$)`)
	detectSSHHost       = regexp.MustCompile(`(?m)^Host\s+`)
	detectSSHHostName   = regexp.MustCompile(`(?m)^\s+HostName\s+`)
	detectSSHUser       = regexp.MustCompile(`(?m)^\s+User\s+`)
	detectSecureCRTHost = regexp.MustCompile(`S:"Hostname"`)
	detectSecureCRTUser = regexp.MustCompile(`S:"Username"`)
	detectSecureCRTList = regexp.MustCompile(`(?i)D:"\[Sessions\]`)
)

// Detect guesses a format from the document contents. It returns "" when no
// format matches, so the UI can ask the user to choose one.
func Detect(text string) Format {
	input := strings.TrimSpace(strings.TrimPrefix(text, "\xef\xbb\xbf"))
	if input == "" {
		return ""
	}

	if detectPuttyRegistry.MatchString(input) {
		return FormatPuTTY
	}

	hasMobaBookmarkSection := detectMobaBookmark.MatchString(input)
	hasMobaMetadata := detectMobaSubRep.MatchString(input) && detectMobaImgNum.MatchString(input)
	hasMobaSession := detectMobaSession.MatchString(input)
	hasMobaFullConfig := detectMobaMisc.MatchString(input) && detectMobaPasswords.MatchString(input)
	if strings.Contains(strings.ToLower(input), "[mobaxterm]") ||
		(hasMobaBookmarkSection && (hasMobaMetadata || hasMobaSession || hasMobaFullConfig)) ||
		(hasMobaFullConfig && hasMobaBookmarkSection) {
		return FormatMobaXterm
	}

	if detectSSHHost.MatchString(input) &&
		(detectSSHHostName.MatchString(input) || detectSSHUser.MatchString(input)) {
		return FormatSSHConfig
	}

	if detectSecureCRTHost.MatchString(input) &&
		(detectSecureCRTUser.MatchString(input) || detectSecureCRTList.MatchString(input)) {
		return FormatSecureCRT
	}

	var candidate map[string]any
	if json.Unmarshal([]byte(input), &candidate) == nil && candidate != nil {
		if _, ok := candidate["conection_type"]; ok {
			if _, hasHost := candidate["host"].(string); hasHost {
				return FormatFinalShell
			}
		}
	}

	if !strings.HasPrefix(input, "{") && !strings.HasPrefix(input, "[") {
		firstLine := input
		if index := strings.IndexByte(firstLine, '\n'); index >= 0 {
			firstLine = firstLine[:index]
		}
		lower := strings.ToLower(firstLine)
		if (strings.Contains(lower, "hostname") || strings.Contains(lower, "host") ||
			strings.Contains(lower, "server")) &&
			(strings.Contains(firstLine, ",") || strings.Contains(firstLine, "\t")) {
			return FormatCSV
		}
	}
	return ""
}
