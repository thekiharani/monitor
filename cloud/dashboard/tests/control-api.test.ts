import { afterEach, describe, expect, it, vi } from "vitest";
import {
  controlListPath,
  getControlAdmission,
  hasControlCapability,
  type ControlSession,
} from "@/control-api";

function session(capabilities: string[]): ControlSession {
  return {
    principal_id: "principal",
    account_id: "account",
    email: "operator@example.test",
    capabilities,
    known_capabilities: [],
  };
}

describe("hasControlCapability", () => {
  it("denies missing sessions and capabilities", () => {
    expect(hasControlCapability(null, "platform.accounts.read")).toBe(false);
    expect(hasControlCapability(session([]), "platform.accounts.read")).toBe(false);
  });

  it("accepts exact and platform-wide grants", () => {
    expect(
      hasControlCapability(session(["platform.accounts.read"]), "platform.accounts.read"),
    ).toBe(true);
    expect(hasControlCapability(session(["platform.*"]), "platform.billing.reconcile")).toBe(true);
  });
});

describe("controlListPath", () => {
  it("encodes list filters and omits empty values", () => {
    expect(
      controlListPath("/api/control/v1/accounts", {
        q: "owner+ops@example.test",
        status: "",
        limit: 25,
        offset: 0,
      }),
    ).toBe("/api/control/v1/accounts?q=owner%2Bops%40example.test&limit=25&offset=0");
  });
});

function answerSessionWith(status: number, body: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(JSON.stringify(body), { status })),
  );
}

describe("getControlAdmission", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("tells a visitor who must sign in apart from an operator who lacks grants", async () => {
    answerSessionWith(401, { error: { code: "unauthorized" } });
    expect(await getControlAdmission()).toEqual({ state: "unauthenticated" });

    answerSessionWith(403, { error: { code: "forbidden" } });
    expect(await getControlAdmission()).toEqual({ state: "unauthorized" });
  });

  it("admits the session the server already recognised, without asking again", async () => {
    answerSessionWith(200, {
      data: {
        principal_id: "principal",
        account_id: "account",
        email: "operator@example.test",
        capabilities: ["platform.*"],
        known_capabilities: [],
      },
    });

    const admission = await getControlAdmission();
    expect(admission.state).toBe("admitted");
    expect(admission.state === "admitted" && admission.session.email).toBe("operator@example.test");
  });

  it("surfaces a failing server rather than reporting it as a signed-out visitor", async () => {
    answerSessionWith(503, { error: { code: "service_unavailable" } });
    await expect(getControlAdmission()).rejects.toThrow();
  });
});
