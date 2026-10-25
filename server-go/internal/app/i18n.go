package app

// 服务端渲染页面（登录、结账结果、网页账户）的中英文案（TECH_DESIGN §10.3）。
// 桌面端调用的 /v1 接口错误信息保持英文，由客户端自己显示文案。

import (
	"fmt"
	"net/http"
	"strings"
)

type lang string

const (
	langEN lang = "en"
	langZH lang = "zh"
)

const langCookie = "nr_lang"

// pickLang 依次看 ?lang=、语言 cookie、Accept-Language；默认英文（与客户端界面一致）。
func pickLang(r *http.Request) lang {
	if l := r.URL.Query().Get("lang"); l == "zh" || l == "en" {
		return lang(l)
	}
	if c, err := r.Cookie(langCookie); err == nil && (c.Value == "zh" || c.Value == "en") {
		return lang(c.Value)
	}
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag := strings.ToLower(strings.TrimSpace(strings.Split(part, ";")[0]))
		switch {
		case strings.HasPrefix(tag, "zh"):
			return langZH
		case strings.HasPrefix(tag, "en"):
			return langEN
		}
	}
	return langEN
}

// rememberLang 用户用 ?lang= 切换语言后记住选择
func rememberLang(w http.ResponseWriter, r *http.Request) {
	if l := r.URL.Query().Get("lang"); l == "zh" || l == "en" {
		http.SetCookie(w, &http.Cookie{Name: langCookie, Value: l, Path: "/", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode})
	}
}

var messages = map[string][2]string{ // key → {英文, 中文}
	// 邮箱验证码
	"otp.badEmail":   {"Please enter a valid email address.", "邮箱格式不正确。"},
	"otp.disposable": {"Temporary email addresses aren't supported. Please use your regular email.", "暂不支持临时邮箱，请换一个常用邮箱。"},
	"otp.captcha":    {"Verification failed. Please try again.", "人机验证失败，请重试。"},
	"otp.tooFast":    {"Please wait a minute before requesting another code.", "发送太频繁，请 1 分钟后再试。"},
	"otp.tooMany":    {"Too many code requests today. Please try again tomorrow.", "今天请求验证码的次数太多了，请明天再试。"},
	"otp.sendFailed": {"We couldn't send the code. Please try again later.", "验证码发送失败，请稍后再试。"},
	"otp.wrong":      {"That code isn't correct.", "验证码不正确。"},
	"otp.expired":    {"The code has expired. Please request a new one.", "验证码已过期，请重新获取。"},
	"otp.locked":     {"Too many wrong attempts. Please request a new code.", "错误次数太多，请重新获取验证码。"},
	"otp.devCode":    {"Development mode (no email provider): your code is %s", "开发模式（未配置邮件服务）：验证码 %s"},
	"user.suspended": {"This account has been suspended. Please contact support.", "该账号已被停用，请联系客服。"},

	// 桌面端登录页
	"login.title":      {"Sign in", "登录"},
	"login.heading":    {"Sign in to NextReply", "登录 NextReply"},
	"login.intro":      {"Enter your email to get a sign-in code. New accounts get %d free replies.", "输入邮箱获取验证码。新用户注册即送 %d 次回复额度。"},
	"login.email":      {"Email", "邮箱"},
	"login.send":       {"Send code", "获取验证码"},
	"login.sentTo":     {"Code sent to", "验证码已发送到"},
	"login.codeHint":   {"6 digits", "6 位数字"},
	"login.submit":     {"Sign in", "登录"},
	"login.back":       {"Use a different email", "换个邮箱"},
	"login.failed":     {"Request failed, please try again.", "请求失败，请稍后再试"},
	"login.done":       {"Signed in", "登录成功"},
	"login.doneMsg":    {"Returning to NextReply. You can close this page.", "正在返回 NextReply，可以关闭此页面。"},
	"login.badLink":    {"Invalid sign-in link", "登录链接无效"},
	"login.badLinkMsg": {"Please go back to NextReply and click Sign in again.", "请回到 NextReply 重新点击登录。"},

	// 结账 / 管理订阅
	"bill.badLink":     {"Invalid link", "链接无效"},
	"bill.unknownPlan": {"Unknown plan.", "未知的套餐。"},
	"bill.expired":     {"Link expired", "链接已失效"},
	"bill.expiredMsg":  {"Please start again from your account.", "请回到账户页重新操作。"},
	"bill.unavailable": {"Not available", "暂不可用"},
	"bill.planClosed":  {"This plan isn't available for purchase yet.", "该套餐尚未开放购买。"},
	"bill.payDown":     {"Payments are temporarily unavailable. Please try again later.", "支付服务暂时不可用，请稍后再试。"},
	"bill.payOff":      {"Payments aren't enabled yet.", "支付服务尚未开通。"},
	"bill.devOnly":     {"Only available in development mode.", "仅开发模式可用。"},
	"bill.noSub":       {"No subscription", "没有订阅"},
	"bill.noSubMsg":    {"This account doesn't have a subscription to manage.", "当前账号没有可管理的订阅。"},
	"bill.cancelSet":   {"Cancellation scheduled", "已设置到期后取消"},
	"bill.cancelMsg":   {"You can keep using your remaining replies until the end of this billing period.", "本周期内仍可使用剩余额度。"},
	"bill.ended":       {"Subscription ended", "订阅已结束"},
	"bill.endedMsg":    {"You're back on the free plan and subscription replies were removed.", "已回到免费套餐，订阅积分已收回。"},
	"bill.success":     {"Payment successful", "支付成功"},
	"bill.successMsg":  {"Your subscription is active. Replies may take a few seconds to appear.", "订阅已开通，积分可能需要几秒钟到账。"},
	"bill.returned":    {"You're back", "已返回"},
	"bill.returnedMsg": {"Refresh NextReply or your account page to see the latest status.", "刷新 NextReply 或账户页即可看到最新状态。"},
	"bill.cancelled":   {"Payment cancelled", "已取消支付"},
	"bill.noCharge":    {"You haven't been charged.", "没有产生任何扣费。"},
	"bill.mockTitle":   {"Mock payment", "模拟支付"},
	"bill.mockHeading": {"Mock payment (development mode)", "模拟支付（开发模式）"},
	"bill.mockIntro":   {"Stripe isn't configured. This simulates a subscription payment; nothing is charged.", "未配置 Stripe，这里模拟一次订阅付款，不会产生真实扣费。"},
	"bill.mockPlan":    {"%d replies per month, up to %d per day", "每月 %d 次，每天最多 %d 次"},
	"bill.mockPay":     {"Simulate successful payment", "模拟支付成功"},
	"bill.mockManage":  {"Manage subscription (development mode)", "管理订阅（开发模式）"},
	"bill.mockCurrent": {"Current: %s · %s%s", "当前：%s · %s%s"},
	"bill.mockEnding":  {" · cancels at period end", " · 到期后取消"},
	"bill.mockCancel":  {"Cancel at period end", "到期后取消订阅"},
	"bill.mockExpire":  {"End now (simulate expiry / refund)", "立即结束（模拟到期 / 退款）"},
	"bill.backAccount": {"Back to your account", "返回账户页"},

	// 网页账户
	"acct.title":          {"Account", "账户"},
	"acct.signInIntro":    {"Sign in with your email to see your plan and replies, manage your subscription or delete your account.", "用邮箱登录，查看套餐和剩余次数、管理订阅或删除账号。"},
	"acct.newAccount":     {"New here? Signing in creates your account. Sign in from the desktop app too to get your sign-up bonus.", "第一次使用？登录即创建账号。在桌面应用中登录后即可领取注册赠送。"},
	"acct.signedInAs":     {"Signed in as", "当前登录"},
	"acct.plan":           {"Plan", "套餐"},
	"acct.replies":        {"Replies left", "剩余次数"},
	"acct.today":          {"Used today", "今日已用"},
	"acct.of":             {"of %d today", "今日上限 %d"},
	"acct.last30":         {"Last 30 days", "近 30 天"},
	"acct.repliesN":       {"%d replies", "%d 次"},
	"acct.buckets":        {"Where your replies come from", "次数明细"},
	"acct.expires":        {"expires %s", "%s 到期"},
	"acct.noExpiry":       {"never expires", "不过期"},
	"acct.noCredits":      {"No replies left.", "没有剩余次数。"},
	"acct.subscription":   {"Subscription", "订阅"},
	"acct.renews":         {"Renews on %s", "%s 自动续费"},
	"acct.ends":           {"Ends on %s", "%s 到期后结束"},
	"acct.pastDue":        {"Payment failed — please update your payment method.", "扣款失败，请更新付款方式。"},
	"acct.manage":         {"Manage subscription", "管理订阅"},
	"acct.upgrade":        {"Upgrade", "升级"},
	"acct.upgradeTo":      {"Upgrade to %s — $%.2f / month", "升级到 %s · $%.2f / 月"},
	"acct.upgradeDesc":    {"%d replies per month, up to %d per day", "每月 %d 次，每天最多 %d 次"},
	"acct.devices":        {"Devices", "设备"},
	"acct.device":         {"Device …%s", "设备 …%s"},
	"acct.lastActive":     {"last active %s", "最近活跃 %s"},
	"acct.signedIn":       {"signed in", "已登录"},
	"acct.signedOut":      {"signed out", "未登录"},
	"acct.signOutDevice":  {"Sign out", "下线"},
	"acct.noDevices":      {"No desktop devices yet. Download NextReply and sign in from Settings → Account.", "还没有桌面设备。下载 NextReply 后在 Settings → Account 中登录。"},
	"acct.deviceOut":      {"The device was signed out.", "该设备已下线。"},
	"acct.logout":         {"Sign out of this browser", "退出网页登录"},
	"acct.danger":         {"Delete account", "删除账号"},
	"acct.dangerIntro":    {"Permanently delete your account and personal data.", "永久删除你的账号和个人数据。"},
	"acct.deleteTitle":    {"Delete your account", "删除账号"},
	"acct.deleteWhat":     {"This permanently deletes your account. It can't be undone.", "此操作会永久删除你的账号，无法恢复。"},
	"acct.deleteItems":    {"Your email address and sign-ins are deleted; all your devices are signed out.|Your remaining replies are removed.|An active subscription is cancelled immediately and won't renew. Refunds follow our refund policy.|Usage records are kept without your email, IP or device, for accounting and abuse prevention.", "删除你的邮箱和登录信息，所有设备下线。|剩余次数作废。|如有订阅，会立即取消且不再续费；退款按退款政策处理。|使用记录会去掉邮箱、IP 和设备信息后保留，用于对账和防滥用。"},
	"acct.deleteConfirm":  {"Type your email address to confirm", "输入你的邮箱以确认"},
	"acct.deleteButton":   {"Delete my account", "删除我的账号"},
	"acct.cancel":         {"Cancel", "取消"},
	"acct.deleteMismatch": {"The email doesn't match. Your account was not deleted.", "邮箱不一致，账号未删除。"},
	"acct.deleteSubFail":  {"We couldn't cancel your subscription with our payment provider, so your account was not deleted. Please try again later or contact support.", "无法在支付平台取消你的订阅，账号未删除。请稍后再试或联系客服。"},
	"acct.deleted":        {"Account deleted", "账号已删除"},
	"acct.deletedMsg":     {"Your account and personal data have been deleted. Thanks for trying NextReply.", "你的账号和个人数据已删除。感谢你使用 NextReply。"},
	"acct.badForm":        {"Your session has expired. Please reload the page and try again.", "页面已过期，请刷新后重试。"},
	"acct.home":           {"NextReply website", "NextReply 官网"},
	"acct.switchLang":     {"中文", "English"},
	"acct.paidViaApp":     {"Upgrade or manage your plan here or in the app (Settings → Account).", "可以在这里或应用的 Settings → Account 中升级、管理套餐。"},
	"acct.signedOutMsg":   {"You've signed out of this browser.", "已退出网页登录。"},

	// 套餐 / 次数桶名称
	"plan.trial":          {"Trial", "体验"},
	"plan.free":           {"Free", "免费"},
	"plan.pro":            {"Pro", "Pro"},
	"plan.pro_plus":       {"Pro+", "Pro+"},
	"bucket.trial":        {"Trial", "体验额度"},
	"bucket.bonus":        {"Bonus", "赠送"},
	"bucket.daily":        {"Today's free replies", "今日免费次数"},
	"bucket.subscription": {"Subscription", "订阅"},
	"bucket.topup":        {"Top-up", "加购"},
}

func tr(l lang, key string, args ...any) string {
	m, ok := messages[key]
	if !ok {
		return key
	}
	s := m[0]
	if l == langZH {
		s = m[1]
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}
