import { describe, expect, it } from "vitest";
import { issueToken, verifyToken } from "../src/auth";

const SECRET = "test-secret-0123456789";
const DEVICE = "3f1c2b7e-9d4a-4c1e-8b2a-6f5d4e3c2b1a";

describe("device token", () => {
  it("round-trips a valid token", async () => {
    const token = await issueToken(SECRET, DEVICE);
    expect(await verifyToken(SECRET, token)).toBe(DEVICE);
  });

  it("rejects a token signed with another secret", async () => {
    const token = await issueToken("other-secret-abcdefgh", DEVICE);
    expect(await verifyToken(SECRET, token)).toBeNull();
  });

  it("rejects a tampered device id", async () => {
    const token = await issueToken(SECRET, DEVICE);
    const [, sig] = token.split(".");
    const forged = `${btoa("aaaaaaaaaaaaaaaaaaaa").replace(/=+$/, "")}.${sig}`;
    expect(await verifyToken(SECRET, forged)).toBeNull();
  });

  it("rejects garbage", async () => {
    expect(await verifyToken(SECRET, "not-a-token")).toBeNull();
    expect(await verifyToken(SECRET, "a.b.c")).toBeNull();
  });
});
