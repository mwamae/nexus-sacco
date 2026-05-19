// Transactional email templates. Kept as plain Go strings — no template
// engine yet. Each helper returns a populated Message.

package email

import (
	"fmt"
	"strings"
)

// OTPMessage builds an OTP email for either a login challenge or an
// MFA enable confirmation.
func OTPMessage(to, displayName, code, purpose string, ttlMinutes int) Message {
	subject := "Your nexusSacco verification code"
	intro := "Use this code to finish signing in:"
	if purpose == "enable_mfa" {
		subject = "Confirm two-factor authentication"
		intro = "Use this code to enable two-factor authentication on your account:"
	}

	greeting := "Hi"
	if displayName != "" {
		greeting = "Hi " + firstName(displayName)
	}

	text := fmt.Sprintf(
		"%s,\n\n%s\n\n    %s\n\nThis code expires in %d minutes. "+
			"If you didn't ask for it, you can safely ignore this email.\n\n— nexusSacco\n",
		greeting, intro, code, ttlMinutes,
	)

	html := fmt.Sprintf(`<!doctype html>
<html><body style="font-family:-apple-system,Segoe UI,Roboto,sans-serif;background:#f6f4ef;padding:32px;margin:0;color:#29261b">
  <div style="max-width:480px;margin:0 auto;background:#ffffff;border:1px solid #e5e0d4;border-radius:10px;padding:28px">
    <div style="font-family:'IBM Plex Mono',monospace;font-weight:600;font-size:12px;color:#1F8A5B;letter-spacing:.06em;text-transform:uppercase">nexusSacco</div>
    <h1 style="font-size:18px;margin:8px 0 14px">%s</h1>
    <p style="font-size:14px;line-height:1.55;margin:0 0 18px">%s, %s</p>
    <div style="font-family:'IBM Plex Mono',ui-monospace,Menlo,monospace;font-size:26px;font-weight:600;letter-spacing:.18em;padding:14px 16px;background:#f6f4ef;border:1px dashed #c9c3b5;border-radius:8px;text-align:center;color:#1F8A5B">%s</div>
    <p style="font-size:12.5px;line-height:1.55;margin:16px 0 6px;color:#6b6557">This code expires in %d minutes. If you didn't ask for it, you can safely ignore this email.</p>
    <p style="font-size:11px;color:#9a9587;margin-top:24px">Sent by nexusSacco · Do not reply</p>
  </div>
</body></html>`,
		subject, greeting, intro, code, ttlMinutes,
	)

	return Message{
		To:      []string{to},
		Subject: subject,
		Text:    text,
		HTML:    html,
	}
}

func firstName(full string) string {
	if i := strings.IndexByte(full, ' '); i > 0 {
		return full[:i]
	}
	return full
}

// PasswordResetMessage builds the email sent when a user requests a
// password reset. The reset URL embeds an opaque single-use token.
func PasswordResetMessage(to, displayName, resetURL string, ttlMinutes int) Message {
	greeting := "Hi"
	if displayName != "" {
		greeting = "Hi " + firstName(displayName)
	}

	text := fmt.Sprintf(
		"%s,\n\nWe received a request to reset your nexusSacco password. "+
			"Open the link below to set a new one:\n\n    %s\n\n"+
			"This link expires in %d minutes and can be used once. "+
			"If you didn't ask for this, you can safely ignore this email — "+
			"your password won't change.\n\n— nexusSacco\n",
		greeting, resetURL, ttlMinutes,
	)

	html := fmt.Sprintf(`<!doctype html>
<html><body style="font-family:-apple-system,Segoe UI,Roboto,sans-serif;background:#f6f4ef;padding:32px;margin:0;color:#29261b">
  <div style="max-width:480px;margin:0 auto;background:#ffffff;border:1px solid #e5e0d4;border-radius:10px;padding:28px">
    <div style="font-family:'IBM Plex Mono',monospace;font-weight:600;font-size:12px;color:#1F8A5B;letter-spacing:.06em;text-transform:uppercase">nexusSacco</div>
    <h1 style="font-size:18px;margin:8px 0 14px">Reset your password</h1>
    <p style="font-size:14px;line-height:1.55;margin:0 0 18px">%s, we received a request to reset your password. Click the button to set a new one.</p>
    <p style="text-align:center;margin:22px 0">
      <a href="%s" style="display:inline-block;background:#1F8A5B;color:#fff;text-decoration:none;font-weight:600;font-size:14px;padding:12px 22px;border-radius:8px">Set a new password</a>
    </p>
    <p style="font-size:12px;line-height:1.55;margin:14px 0 4px;color:#6b6557">Or paste this URL into your browser:</p>
    <p style="font-family:'IBM Plex Mono',ui-monospace,Menlo,monospace;font-size:11px;word-break:break-all;color:#1F8A5B;margin:0">%s</p>
    <p style="font-size:12.5px;line-height:1.55;margin:18px 0 4px;color:#6b6557">This link expires in %d minutes and can only be used once. If you didn't request a reset, ignore this email — your password won't change.</p>
    <p style="font-size:11px;color:#9a9587;margin-top:24px">Sent by nexusSacco · Do not reply</p>
  </div>
</body></html>`,
		greeting, resetURL, resetURL, ttlMinutes,
	)

	return Message{
		To:      []string{to},
		Subject: "Reset your nexusSacco password",
		Text:    text,
		HTML:    html,
	}
}
