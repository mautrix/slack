package connector

import (
	"go.mau.fi/util/l10n"
)

var L = l10n.NewNamespace("mautrix-slack")

var (
	msgFlowEmailName        = L.Define("login.flow.email.name", "Email")
	msgFlowEmailDescription = L.Define("login.flow.email.description", "Sign in with your Slack email address")
	msgFlowTokenName        = L.Define("login.flow.token.name", "Auth token & cookie")
	msgFlowTokenDescription = L.Define("login.flow.token.description", "Log in with an auth token (and a cookie, if the token is from a browser)")
	msgFlowAppName          = L.Define("login.flow.app.name", "Slack app")
	msgFlowAppDescription   = L.Define("login.flow.app.description", "Log in with a Slack app")

	msgFieldEmailName            = L.Define("login.field.email.name", "Email")
	msgFieldCodeName             = L.Define("login.field.code.name", "Confirmation code")
	msgFieldCodeDescription      = L.Define("login.field.code.description", "Six-character code from Slack")
	msgFieldWorkspaceName        = L.Define("login.field.workspace.name", "Workspace")
	msgFieldWorkspaceDescription = L.Define("login.field.workspace.description", "Slack workspace to connect")
	msgFieldTwoFactorName        = L.Define("login.field.two_factor_code.name", "Authentication code")
	msgFieldTwoFactorDescription = L.Define("login.field.two_factor_code.description", "Six-digit code from your Slack authenticator or SMS")
	msgFieldBotTokenName         = L.Define("login.field.bot_token.name", "Bot token")
	msgFieldBotTokenDescription  = L.Define("login.field.bot_token.description", "Slack bot token for the workspace (starts with `xoxb-`)")
	msgFieldAppTokenName         = L.Define("login.field.app_token.name", "App token")
	msgFieldAppTokenDescription  = L.Define("login.field.app_token.description", "Slack app-level token (starts with `xapp-`)")

	msgStepEmailInstructions     = L.Define("login.step.email.instructions", "Enter the email address associated with your Slack account.")
	msgStepEmailInvalid          = L.Define("login.step.email.instructions.invalid_email", "Enter a valid Slack account email address.")
	msgStepEmailRejected         = L.Define("login.step.email.instructions.email_rejected", "Slack rejected that email address. Check it and try again.")
	msgStepEmailRateLimited      = L.Define("login.step.email.instructions.rate_limited", "Slack is rate limiting email sign-in. Wait a few minutes before trying again.")
	msgStepEmailCaptchaRequired  = L.Define("login.step.email.instructions.captcha_required", "Slack requires a CAPTCHA for this sign-in. Complete the embedded challenge and try again.")
	msgStepEmailStartFailed      = L.Define("login.step.email.instructions.start_failed", "Slack could not start email sign-in. Try again later.")
	msgStepEmailCodeExpired      = L.Define("login.step.email.instructions.code_expired", "That Slack confirmation code expired. Enter your email to request a new one.")
	msgStepEmailNoWorkspaces     = L.Define("login.step.email.instructions.no_workspaces", "Slack did not return any workspaces that support email sign-in for this account.")
	msgStepEmailValidationFailed = L.Define("login.step.email.instructions.validation_failed", "Slack created a session, but the bridge could not validate it. Enter your email to try again.")
	msgStepCaptchaInstructions   = L.Define("login.step.email_captcha.instructions", "Slack requires a CAPTCHA before it can email the confirmation code. Complete the embedded challenge to continue.")
	msgStepCaptchaPending        = L.Define("login.step.email_captcha.instructions.pending", "Complete the embedded CAPTCHA before continuing.")
	msgStepCaptchaNoSolution     = L.Define("login.step.email_captcha.instructions.no_solution", "The CAPTCHA did not return a solution. Complete a new embedded challenge to continue.")
	msgStepCaptchaRejected       = L.Define("login.step.email_captcha.instructions.rejected", "Slack rejected or expired that CAPTCHA solution. Complete a new embedded challenge to continue.")
	msgStepCaptchaRateLimited    = L.Define("login.step.email_captcha.instructions.rate_limited", "Slack is rate limiting email sign-in. Wait a few minutes, then complete a new embedded challenge.")
	msgStepEmailCodeInstructions = L.Define("login.step.email_code.instructions", "Slack emailed you a confirmation code. Enter the six characters from that email.")
	msgStepEmailCodeInvalid      = L.Define("login.step.email_code.instructions.invalid_code", "Enter the six-character code Slack emailed you.")
	msgStepEmailCodeRejected     = L.Define("login.step.email_code.instructions.rejected", "Slack rejected that confirmation code. Check the email and try again.")
	msgStepEmailCodeRateLimited  = L.Define("login.step.email_code.instructions.rate_limited", "Slack is rate limiting code checks. Wait before trying again.")
	msgStepWorkspaceInstructions = L.Define("login.step.workspace.instructions", "Choose the Slack workspace to connect.")
	msgStepWorkspaceInvalid      = L.Define("login.step.workspace.instructions.invalid", "Choose one of the Slack workspaces in the list.")
	msgStepWorkspaceSignInFailed = L.Define(
		"login.step.workspace.instructions.sign_in_failed", "Could not sign in to {workspace}: {error}",
		l10n.WithComment("error is an English description of the failure"),
	)
	msgStepWorkspaceTwoFactorFailed = L.Define(
		"login.step.workspace.instructions.two_factor_failed", "Could not start two-factor authentication for {workspace}: {error}",
		l10n.WithComment("error is an English description of the failure"),
	)
	msgStepTwoFactorInstructions = L.Define("login.step.two_factor.instructions", "Enter the authentication code required by this Slack workspace.")
	msgStepTwoFactorExpired      = L.Define("login.step.two_factor.instructions.expired", "That two-factor session expired. Enter a new authentication code.")
	msgStepTwoFactorInvalid      = L.Define("login.step.two_factor.instructions.invalid_code", "Enter the six-digit code from your authenticator app or SMS.")
	msgStepTwoFactorRateLimited  = L.Define("login.step.two_factor.instructions.rate_limited", "Slack is rate limiting authentication-code checks. Wait before trying again.")
	msgStepTwoFactorRejected     = L.Define("login.step.two_factor.instructions.rejected", "Slack rejected that authentication code. Check the code and try again.")
	msgStepTwoFactorFailed       = L.Define("login.step.two_factor.instructions.failed", "Slack did not complete two-factor authentication. Check the code and try again.")
	msgStepBrowserInstructions   = L.Define("login.step.browser.instructions", "{workspace} doesn't support email sign-in. Sign in to it in the browser to continue.")
	msgStepBrowserSSO            = L.Define("login.step.browser.instructions.sso", "{workspace} uses single sign-on. Sign in with your organization's account in the browser to continue.")
	msgStepAuthTokenInstructions = L.Define("login.step.auth_token.instructions", "Enter a JSON object with your auth token and cookie token, or a cURL command copied from browser devtools.\n\nFor example: `{\"auth_token\":\"xoxc-...\",\"cookie_token\":\"xoxd-...\"}`")
	msgStepCompleteInstructions  = L.Define(
		"login.step.complete.instructions", "Successfully logged into {workspace} as {user}",
		l10n.WithComment("user is the Slack account's email address or username"),
	)
)
