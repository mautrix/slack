config => {
  if (!window.__mautrixSlackCaptchaPromise) {
    window.__mautrixSlackCaptchaPromise = new Promise((resolve, reject) => {
      window.__mautrixSlackRecaptchaLoaded = () => {
        const overlay = document.createElement('div')
        // Google's reCAPTCHA challenge popup uses z-index 2000000000.
        overlay.style.cssText = 'position:fixed;inset:0;z-index:1999999999;' +
          'background:#fff;display:flex;align-items:center;' +
          'justify-content:center;padding:2rem'

        const content = document.createElement('div')
        content.style.cssText = 'display:flex;flex-direction:column;gap:1rem;' +
          'align-items:center;text-align:center;font:16px sans-serif;color:#1d1c1d'

        const heading = document.createElement('strong')
        heading.textContent = 'Complete the Slack verification'
        const challenge = document.createElement('div')
        content.append(heading, challenge)
        overlay.append(content)
        document.body.append(overlay)

        grecaptcha.render(challenge, {
          sitekey: config.siteKey,
          callback: token => resolve({ captcha_token: token }),
          'error-callback': () => reject(new Error('Slack reCAPTCHA failed')),
          'expired-callback': () => reject(new Error('Slack reCAPTCHA token expired')),
        })
      }

      const script = document.createElement('script')
      script.src = 'https://www.google.com/recaptcha/api.js?render=explicit&onload=__mautrixSlackRecaptchaLoaded'
      script.async = true
      script.defer = true
      script.onerror = () => reject(new Error('failed to load Slack reCAPTCHA'))
      document.head.append(script)
    })
  }

  return window.__mautrixSlackCaptchaPromise
}
