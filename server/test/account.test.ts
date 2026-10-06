import { describe, expect, it } from "vitest";
import { pkceChallenge, safeEqual } from "../src/account/crypto";
import { isDisposable, isValidEmail, normalizeEmail } from "../src/account/email";
import { planFromSubscription, type SubscriptionRow } from "../src/account/identity";

describe("email", () => {
  it("normalizes gmail dots, plus suffixes and case", () => {
    expect(normalizeEmail("Jane.Doe+promo@GMail.com")).toBe("janedoe@gmail.com");
    expect(normalizeEmail("jane.doe+x@googlemail.com")).toBe("janedoe@gmail.com");
    // 非 gmail 不去点号
    expect(normalizeEmail("jane.doe+x@outlook.com")).toBe("jane.doe@outlook.com");
  });

  it("validates and flags disposable domains", () => {
    expect(isValidEmail("a@b.co")).toBe(true);
    expect(isValidEmail("not-an-email")).toBe(false);
    expect(isDisposable("x@mailinator.com")).toBe(true);
    expect(isDisposable("x@gmail.com")).toBe(false);
  });
});

describe("pkce", () => {
  it("matches the RFC 7636 example", async () => {
    expect(await pkceChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")).toBe("E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM");
  });

  it("compares in constant time", () => {
    expect(safeEqual("abc", "abc")).toBe(true);
    expect(safeEqual("abc", "abd")).toBe(false);
    expect(safeEqual("abc", "abcd")).toBe(false);
  });
});

describe("plan resolution", () => {
  const now = Date.UTC(2026, 9, 6);
  const sub = (o: Partial<SubscriptionRow>): SubscriptionRow => ({
    plan: "pro", status: "active", provider: "stripe", customer_id: null, subscription_id: null,
    current_period_start: now - 864e5, current_period_end: now + 864e5, cancel_at_period_end: 0, ...o,
  });

  it("uses the paid plan while active or past due within grace", () => {
    expect(planFromSubscription(sub({}), now)).toBe("pro");
    expect(planFromSubscription(sub({ status: "past_due", current_period_end: now - 864e5 }), now)).toBe("pro");
  });

  it("falls back to free when canceled or long expired", () => {
    expect(planFromSubscription(null, now)).toBe("free");
    expect(planFromSubscription(sub({ status: "canceled" }), now)).toBe("free");
    expect(planFromSubscription(sub({ current_period_end: now - 10 * 864e5 }), now)).toBe("free");
  });
});
