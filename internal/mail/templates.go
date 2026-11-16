package mail

import (
	"fmt"
	"html"
)

// The mails' text, in English: plain text plus a simple HTML version. site
// is the product's name as the deployment calls it (SITE_NAME), "" meaning
// DefaultSiteName.

// DefaultSiteName is the name when SITE_NAME is not set; the config's
// envDefault spells the same value.
const DefaultSiteName = "HLS VOD"

func siteName(site string) string {
	if site == "" {
		return DefaultSiteName
	}
	return site
}

// signature closes every mail: the plain-text line and the HTML paragraph.
func signature(site string) (string, string) {
	return "\n-- \n" + site + "\n", html.EscapeString(site)
}

func page(paragraphs ...string) string {
	out := `<!doctype html><html><body style="font-family:sans-serif;line-height:1.5;max-width:560px">`
	for _, p := range paragraphs {
		out += "<p>" + p + "</p>"
	}
	return out + "</body></html>"
}

func button(link, label string) string {
	l := html.EscapeString(link)
	return fmt.Sprintf(`<a href="%s" style="display:inline-block;padding:10px 16px;background:#111;color:#fff;text-decoration:none;border-radius:6px">%s</a><br><br>`+
		`Or open this link: <a href="%s">%s</a>`, l, html.EscapeString(label), l, l)
}

// VerifyMessage asks to confirm the address of a new account. link carries
// the token in its fragment.
func VerifyMessage(site, to, username, link string) Message {
	site = siteName(site)
	sigText, sigHTML := signature(site)
	return Message{
		Kind:    KindVerify,
		To:      to,
		Subject: "Confirm your email address for " + site,
		Text: fmt.Sprintf("Hi %s,\n\nConfirm your email address to finish creating your %s account:\n\n%s\n\n"+
			"The link works once and expires 24 hours after you signed up.\n\n"+
			"If you did not sign up, ignore this mail: the account is deleted unless the address is confirmed.\n", username, site, link) + sigText,
		HTML: page(
			"Hi "+html.EscapeString(username)+",",
			"Confirm your email address to finish creating your "+html.EscapeString(site)+" account:",
			button(link, "Confirm email address"),
			"The link works once and expires 24 hours after you signed up.",
			"If you did not sign up, ignore this mail: the account is deleted unless the address is confirmed.",
			sigHTML,
		),
	}
}

// AccountExistsMessage tells the owner of an address that already has an
// account that someone tried to sign up with it.
func AccountExistsMessage(site, to, loginLink string) Message {
	site = siteName(site)
	sigText, sigHTML := signature(site)
	return Message{
		Kind:    KindAccountExists,
		To:      to,
		Subject: "Someone tried to sign up for " + site + " with your email",
		Text: fmt.Sprintf("Someone tried to create a %s account with this email address, which already has one.\n\n"+
			"If it was you, log in instead:\n\n%s\n\n"+
			"If you have not confirmed the address yet, use \"Resend email\" on the login page.\n\n"+
			"If it was not you, you can ignore this mail: nothing was changed.\n", site, loginLink) + sigText,
		HTML: page(
			"Someone tried to create a "+html.EscapeString(site)+" account with this email address, which already has one.",
			"If it was you, log in instead:",
			button(loginLink, "Log in"),
			`If you have not confirmed the address yet, use "Resend email" on the login page.`,
			"If it was not you, you can ignore this mail: nothing was changed.",
			sigHTML,
		),
	}
}

// ResetMessage carries a password-reset link (token in its fragment) and
// the account's username, which people forget too.
func ResetMessage(site, to, username, link string) Message {
	site = siteName(site)
	sigText, sigHTML := signature(site)
	return Message{
		Kind:    KindReset,
		To:      to,
		Subject: "Reset your " + site + " password",
		Text: fmt.Sprintf("Hi %s,\n\nSomeone asked to reset the password of your %s account (username: %s).\n\n"+
			"Choose a new password here:\n\n%s\n\n"+
			"The link works once and expires in 30 minutes. Resetting logs out every device.\n\n"+
			"If you did not ask for this, ignore this mail: your password stays as it is.\n", username, site, username, link) + sigText,
		HTML: page(
			"Hi "+html.EscapeString(username)+",",
			"Someone asked to reset the password of your "+html.EscapeString(site)+" account (username: <b>"+html.EscapeString(username)+"</b>).",
			button(link, "Choose a new password"),
			"The link works once and expires in 30 minutes. Resetting logs out every device.",
			"If you did not ask for this, ignore this mail: your password stays as it is.",
			sigHTML,
		),
	}
}

// GoogleLinkedMessage tells an account's own address that a Google account
// was connected to it, by the user or by signing in with Google with the
// same email.
func GoogleLinkedMessage(site, to, username, accountLink string) Message {
	site = siteName(site)
	sigText, sigHTML := signature(site)
	return Message{
		Kind:    KindGoogleLinked,
		To:      to,
		Subject: "Google sign-in was connected to your " + site + " account",
		Text: fmt.Sprintf("Hi %s,\n\nA Google account can now sign in to your %s account (username: %s).\n\n"+
			"If this was not you, open your account, remove Google and change your password:\n\n%s\n", username, site, username, accountLink) + sigText,
		HTML: page(
			"Hi "+html.EscapeString(username)+",",
			"A Google account can now sign in to your "+html.EscapeString(site)+" account (username: <b>"+html.EscapeString(username)+"</b>).",
			"If this was not you, open your account, remove Google and change your password:",
			button(accountLink, "Open your account"),
			sigHTML,
		),
	}
}
