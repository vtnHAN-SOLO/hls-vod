# About {{brand}} {#about}

{{brand}} is a small, self-hosted place to watch movies. Each movie is prepared once, when it
is added, as adaptive HLS video in several qualities, and the player picks the one your
connection can carry. Nothing is converted while you watch, so playback stays fast for
everyone.

It is not a commercial service. There are no ads, no subscriptions and no recommendations
based on what you watch: Home shows what is new, what is featured and what other viewers have
been watching, counted per viewer and never shown with names.

## The name and the logo {#name}

The name you see in the header is chosen by whoever runs this site; this one is called
**{{brand}}**. The logo is the dog in the top left corner: select it, or Home, to come back to
the start page.


## Privacy {#privacy}

This site keeps only what it needs to give you an account and remember where you stopped.

### What is stored

- **Your account:** your username, your email address, and your password as an argon2id hash.
  The password itself is never stored. An account made with Google has no password
- **Google sign-in, if you use it:** the account id Google gives this site and the email
  address it had when you linked it
- **Watch progress:** for each movie you started, the position you reached and when
- **Sessions:** one per device you are logged in on, with your browser's name as it reports
  itself, when the session started and when it was last used. You can see them and log them
  out on your Account page. Sessions are deleted a week after they end
- **Mail sent to you:** a one-way hash of your address and the kind of mail, kept two days to
  limit how many mails one address gets. Links in those mails work once and expire

Your IP address is used to slow down password guessing and appears in the server's logs; it
is not stored with your account. Administrators can see usernames, email addresses and roles,
never passwords.

### Cookies and storage

The only cookie is the login cookie, which keeps you logged in for up to 30 days. It cannot be
read by the page's scripts. While a Google sign-in is in progress a second, short-lived cookie
(10 minutes) checks that the answer coming back is the one this site asked for.

The player remembers your volume, speed and subtitle choices in your browser's local storage.
They never leave your device.

There is no tracking, no analytics and no advertising. Fonts and images come from this site
itself; the only outside script is Cloudflare's challenge (below), loaded only on a page that
shows one.

### Outside services

- **Resend** delivers this site's email (verifying your address, password resets). It receives
  your address and the message
- **Cloudflare Turnstile** tells people from bots when you register, ask for a mail, or after
  several failed logins. Cloudflare sees your IP address and some details of your browser while
  it checks
- **Google** is used only if you choose to sign in with Google. Google then knows you signed in
  here
