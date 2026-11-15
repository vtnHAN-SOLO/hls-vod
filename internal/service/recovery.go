package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	pmail "github.com/vtnHAN-SOLO/hls-vod/internal/mail"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// Forgot / reset password. Same rules as registration: links carry their
// token in the URL fragment, the database keeps only its SHA-256, every mail
// goes out in the background through the Outbox, and forgot-password answers
// alike whether or not the address has an account.
//
// An account's email is fixed at registration, like its username: it is the
// recovery channel and the key for linking Google sign-in, so
// there is no email change. Accounts made with useradmin have no email and
// no reset by mail: an admin runs `useradmin set-password`.

// ForgotPassword mails a reset link to the verified account with that
// address, with its username in the mail. nil whether or not there is one:
// an unknown, unverified or disabled address (or one only a useradmin
// account could have, which has none) sends nothing, and the reason goes to
// the log.
func (r *Registration) ForgotPassword(ctx context.Context, emailAddr, captchaToken, clientIP string) error {
	limitKey := "forgot|" + clientIP
	attempt, retry, ok := r.ipLimit.Allow(limitKey)
	if !ok {
		return &ThrottledError{RetryAfter: retry}
	}
	defer attempt.Release()
	email, err := NormalizeEmail(emailAddr)
	if err != nil {
		return err
	}
	attempt.Fail()
	if err := checkCaptcha(ctx, r.captcha, captchaToken, clientIP, ErrCaptchaFailed); err != nil {
		return err
	}
	token, tokenHash, err := auth.NewOpaqueToken()
	if err != nil {
		return err
	}
	u, outcome, err := r.store.IssueResetToken(ctx, email, tokenHash, auth.ResetTTL)
	if err != nil {
		return err
	}
	if outcome != store.ResetIssued {
		r.log.InfoContext(ctx, "password reset: nothing sent", "reason", outcome.String())
		return nil
	}
	r.log.InfoContext(ctx, "password reset link issued", "user_id", u.ID)
	r.send(ctx, pmail.ResetMessage(email, u.Username, r.link("/reset-password", token)))
	return nil
}

// ResetPassword applies a reset link: the new password, every session of
// the user ended, the link used. It starts no session; the user logs in
// with the new password. The password rules come first, so an invalid one
// costs no hash, and so does an unknown, expired or used token
// (ErrInvalidEmailToken, the same for all three).
func (r *Registration) ResetPassword(ctx context.Context, token, newPassword string) error {
	if err := auth.CheckPassword(newPassword); err != nil {
		return &ValidationError{"new_password", err.Error()}
	}
	tokenHash, ok := auth.HashOpaqueToken(token)
	if !ok {
		return ErrInvalidEmailToken
	}
	usable, err := r.store.EmailTokenUsable(ctx, tokenHash, store.PurposeReset)
	if err != nil {
		return err
	}
	if !usable {
		return ErrInvalidEmailToken
	}
	hash, err := r.pw.Hash(ctx, newPassword)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}
	u, err := r.store.ResetPassword(ctx, tokenHash, hash)
	if errors.Is(err, store.ErrEmailTokenInvalid) {
		return ErrInvalidEmailToken
	}
	if err != nil {
		return err
	}
	r.log.InfoContext(ctx, "password reset; every session of the user revoked", "user_id", u.ID)
	return nil
}

// PasswordSetLink is "Set password" for an account without one (Google
// only): a reset link to the account's own verified email, the same token,
// template and mail limits as forgot-password. Refused (a ValidationError)
// when the account has a password: that one is changed with the current
// password instead. nil otherwise, whether or not a mail could go out.
// Setting the password through the link ends every session, as a reset
// does.
func (r *Registration) PasswordSetLink(ctx context.Context, p Principal) error {
	if err := r.auth.requireLiveSession(ctx, p); err != nil {
		return err
	}
	u, err := r.auth.Me(ctx, p)
	if err != nil {
		return err
	}
	if u.HasPassword() {
		return &ValidationError{"password", "the account already has one: change it with the current password"}
	}
	if u.Email == nil || u.EmailVerifiedAt == nil {
		r.log.WarnContext(ctx, "set-password link: nothing sent", "reason", "no verified email", "user_id", u.ID)
		return nil
	}
	token, tokenHash, err := auth.NewOpaqueToken()
	if err != nil {
		return err
	}
	switch err := r.store.IssueResetTokenForUser(ctx, u.ID, tokenHash, auth.ResetTTL); {
	case errors.Is(err, store.ErrAccountDisabled):
		r.log.InfoContext(ctx, "set-password link: nothing sent", "reason", "account disabled", "user_id", u.ID)
		return nil
	case err != nil:
		return err
	}
	r.log.InfoContext(ctx, "set-password link issued", "user_id", u.ID)
	r.send(ctx, pmail.ResetMessage(*u.Email, u.Username, r.link("/reset-password", token)))
	return nil
}
