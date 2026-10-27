import { describe, expect, it } from "vitest";
import { RoleID } from "@/queries/users";
import { makeToken } from "@/test/jwt";
import { decodeAccessToken } from "@/utils/jwt";

describe("decodeAccessToken", () => {
  it("reads the claims", () => {
    expect(
      decodeAccessToken(makeToken("ota@example.com", RoleID.Submitter, 60)),
    ).toMatchObject({ email: "ota@example.com", role_id: RoleID.Submitter });
  });

  it.each([
    ["no payload", "header"],
    ["not base64", "a.!!!.c"],
    ["not JSON", `a.${btoa("nope")}.c`],
    ["missing claims", `a.${btoa(JSON.stringify({ email: "a@b" }))}.c`],
  ])("rejects a token with %s", (_name, token) => {
    expect(decodeAccessToken(token)).toBeNull();
  });
});
