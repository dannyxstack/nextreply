import { describe, expect, it } from "vitest";
import { allocate, endOfUtcDay, spendOrder, summarize, type Grant } from "../src/credits/allocate";

const NOW = Date.UTC(2026, 9, 6, 12);
const g = (id: string, bucket: Grant["bucket"], remaining: number, expires_at: number | null, created_at = 0): Grant => ({
  id, bucket, remaining, expires_at, created_at,
});

describe("credit allocation", () => {
  it("spends the soonest-expiring bucket first, non-expiring last", () => {
    const grants = [g("sub", "subscription", 100, NOW + 20 * 864e5), g("trial", "trial", 5, null), g("daily", "daily", 5, endOfUtcDay(NOW))];
    expect(spendOrder(grants, NOW).map((x) => x.id)).toEqual(["daily", "sub", "trial"]);
  });

  it("skips expired and empty grants", () => {
    const grants = [g("old", "bonus", 10, NOW - 1), g("empty", "daily", 0, NOW + 1000), g("ok", "trial", 1, null)];
    expect(allocate(grants, 1, NOW)).toEqual([{ grantId: "ok", amount: 1 }]);
  });

  it("splits a cost across buckets and refuses partial charges", () => {
    const grants = [g("a", "daily", 1, NOW + 1000), g("b", "bonus", 2, NOW + 5000)];
    expect(allocate(grants, 2, NOW)).toEqual([{ grantId: "a", amount: 1 }, { grantId: "b", amount: 1 }]);
    expect(allocate(grants, 4, NOW)).toBeNull();
  });

  it("summarizes usable credits per bucket", () => {
    const grants = [g("a", "bonus", 3, NOW + 1000), g("b", "bonus", 2, NOW + 9000), g("c", "trial", 4, null), g("x", "daily", 9, NOW - 1)];
    expect(summarize(grants, NOW)).toEqual({
      total: 9,
      buckets: [
        { bucket: "bonus", remaining: 5, expires_at: NOW + 1000 },
        { bucket: "trial", remaining: 4, expires_at: null },
      ],
    });
  });

  it("computes the end of the UTC day", () => {
    expect(endOfUtcDay(NOW)).toBe(Date.UTC(2026, 9, 7));
  });
});
