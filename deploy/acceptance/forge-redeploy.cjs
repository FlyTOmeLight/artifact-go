// Real login and STS file acceptance. No session or temporary credential is persisted.
// First run creates a retained published version; --verify rechecks the same version.
const fs = require("node:fs");
const path = require("node:path");
const os = require("node:os");
const crypto = require("node:crypto");
const assert = require("node:assert/strict");
const { spawnSync } = require("node:child_process");
const root = path.resolve(__dirname, "../..");
const web =
  process.env.FORGE_WEB_ROOT || path.resolve(root, "../forge-tenant-web");
const reportPath = process.env.FORGE_REDEPLOY_REPORT;
let stage = "prepare";
const data = "artifact-go durable OrbStack redeploy acceptance\n";
function check(value, name) {
  if (!value) throw Object.assign(new Error(), { name });
}
async function main() {
  check(reportPath, "ReportPathRequired");
  const config = JSON.parse(
    fs.readFileSync(
      process.env.FORGE_CONFIG ||
        path.resolve(web, "../forge-tenant-api/.local/deployment.json"),
    ),
  );
  const scope = JSON.parse(
    fs.readFileSync(process.env.FORGE_TEST_SCOPE || path.join(__dirname, "forge-scope.json")),
  );
  check(scope, "ConfiguredProjectScopeRequired");
  const headers = {
    "x-forge-tenant-id": scope.TenantID,
    "x-forge-project-id": scope.ProjectID,
  };
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "artifact-transfer-"));
  fs.chmodSync(dir, 0o700);
  const binary = path.join(dir, "transfer");
  let browser;
  let logout;
  try {
    const build = spawnSync(
      "go",
      ["build", "-o", binary, "deploy/acceptance/forge-transfer/main.go"],
      { cwd: root, encoding: "utf8", timeout: 120000 },
    );
    check(build.status === 0, "TransferBuildFailed");
    const { chromium } = require(path.join(web, "node_modules/playwright"));
    stage = "launch";
    browser = await chromium.launch({
      executablePath:
        process.env.CHROME_EXECUTABLE ||
        "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
      headless: true,
      args: ["--no-proxy-server"],
    });
    const context = await browser.newContext();
    context.setDefaultTimeout(30000);
    const page = await context.newPage();
    const login = [];
    page.on("response", (r) => {
      const p = new URL(r.url()).pathname;
      if (["/api/login", "/auth/callback"].includes(p))
        login.push({ path: p, status: r.status() });
    });
    if (process.argv.includes("--restart")) {
      stage = "restart";
      for (const args of [
        ["rollout", "restart", "deployment/artifact"],
        ["rollout", "status", "deployment/artifact", "--timeout=180s"],
      ]) {
        const result = spawnSync("kubectl", ["--context", "orbstack", "-n", "forge-tenant-split", ...args], { encoding: "utf8", timeout: 190000 });
        check(result.status === 0, "RestartFailed");
      }
      console.log(JSON.stringify({ restartReadyAt: new Date().toISOString() }));
    }
    stage = "login-page";
    await page.goto(config.publicURL);
    stage = "login-form";
    await page.locator("#username").fill(config.adminUsername);
    await page.locator("#password").fill(config.adminPassword);
    await page.locator("#submit").click();
    await page.waitForURL((u) => u.pathname === "/");
    check(
      login.some((r) => r.path === "/api/login" && r.status === 200) &&
        login.some((r) => r.path === "/auth/callback" && r.status === 303),
      "RealLoginCallbackRequired",
    );
    stage = "me";
    const meResponse = await context.request.get(
      config.publicURL + "/api/v1/me",
    );
    check(meResponse.status() === 200, "MeFailed");
    const me = await meResponse.json();
    logout = async () => {
      const response = await context.request.post(
        config.publicURL + "/auth/logout",
        {
          headers: { Origin: config.publicURL, "X-CSRF-Token": me.csrf_token },
        },
      );
      check(response.status() === 200, "LogoutFailed");
    };
    async function request(
      method,
      url,
      body,
      expected = 200,
      custom = headers,
    ) {
      stage = method + " " + url;
      const result = await page.evaluate(
        async ({ method, url, body, headers, csrf }) => {
          const started = performance.now();
          let status;
          let contentType;
          try {
            const r = await fetch(url, {
              method,
              signal: AbortSignal.timeout(30000),
              headers: {
                "Content-Type": "application/json",
                "X-CSRF-Token": csrf,
                ...headers,
              },
              ...(body === undefined ? {} : { body: JSON.stringify(body) }),
            });
            status = r.status;
            contentType = (r.headers.get("content-type") || "").split(";")[0];
            return { status, contentType, body: await r.json(), durationMs: Math.round(performance.now() - started) };
          } catch (error) {
            // Never return exception messages, headers, or non-JSON response bodies.
            const errorType = ["TimeoutError", "AbortError", "TypeError", "SyntaxError"].includes(error.name) ? error.name : "RequestError";
            return { status, contentType, errorType, durationMs: Math.round(performance.now() - started) };
          }
        },
        { method, url, body, headers: custom, csrf: me.csrf_token },
      );
      const diagnostic = { method, path: url, status: result.status, contentType: result.contentType, durationMs: result.durationMs, errorType: result.errorType };
      if (process.env.FORGE_DIAGNOSTICS === "1") console.log(JSON.stringify({ request: diagnostic }));
      if (result.errorType || result.status !== expected)
        throw Object.assign(new Error(), {
          name: result.errorType || "HTTPStatusMismatch",
          diagnostic,
        });
      return result.body;
    }
    function transfer(operation, grant) {
      stage = "STS " + operation;
      const result = spawnSync(binary, [], {
        input: JSON.stringify({ operation, grant, data }),
        encoding: "utf8",
        timeout: 40000,
      });
      check(result.status === 0, "STSTransferFailed");
    }
    const createGrant = (fileID) =>
      request("POST", "/storage/v1/storage/grants", {
        fileIDs: [fileID],
        scope: { actions: ["s3:GetObject"] },
      });
    let report;
    if (process.argv.includes("--verify")) {
      report = JSON.parse(fs.readFileSync(reportPath));
      assert.deepEqual(report.scope, scope);
    } else {
      check(!fs.existsSync(reportPath), "ReportAlreadyExistsUseVerify");
      const registered = await request("POST", "/storage/v1/storage/files", [
        {
          requestKey: crypto.randomUUID(),
          size: Buffer.byteLength(data),
          contentType: "text/plain",
        },
      ]);
      check(
        registered.length === 1 && !registered[0].error && registered[0].fileID,
        "FileRegistrationFailed",
      );
      const fileID = registered[0].fileID;
      const upload = await request("POST", "/storage/v1/storage/grants", {
        fileIDs: [fileID],
        scope: { actions: ["s3:PutObject"] },
      });
      transfer("put", upload);
      const complete = await request(
        "POST",
        "/storage/v1/storage/files/complete",
        [fileID],
      );
      check(
        complete.length === 1 &&
          !complete[0].error &&
          complete[0].state === "ready",
        "FileCompleteFailed",
      );
      const artifact = await request(
        "POST",
        "/artifact/v1/artifacts",
        {
          type: "model",
          name: "redeploy-" + crypto.randomUUID(),
          versionScheme: "integer",
        },
        201,
      );
      const version = await request(
        "POST",
        `/artifact/v1/artifacts/${artifact.id}/versions`,
        {
          requestKey: crypto.randomUUID(),
          payload: { acceptance: "durable-redeploy" },
          files: [{ fileID, path: "acceptance.txt" }],
        },
        201,
      );
      report = {
        scope,
        artifactID: artifact.id,
        versionID: version.id,
        fileID,
        createdAt: new Date().toISOString(),
      };
      // Persist only public IDs, so --verify can prove the same bytes survive another deployment.
      fs.writeFileSync(reportPath, JSON.stringify(report, null, 2) + "\n", {
        flag: "wx",
        mode: 0o600,
      });
    }
    const version = await request(
      "GET",
      `/artifact/v1/artifacts/${report.artifactID}/versions/${report.versionID}`,
    );
    check(
      version.state === "ready" &&
        version.files.some((f) => f.fileID === report.fileID),
      "PublishedFileReferenceMissing",
    );
    transfer("get", await createGrant(report.fileID));
    const tombstone = await request(
      "GET",
      "/storage/v1/storage/files/93db21eb-f525-4750-bf86-279539144072",
    );
    check(tombstone.state === "deleted", "OldFileNotDeleted");
    await request(
      "POST",
      "/storage/v1/storage/grants",
      { fileIDs: [tombstone.fileID], scope: { actions: ["s3:GetObject"] } },
      409,
    );
    await request(
      "POST",
      "/artifact/v1/artifacts",
      { type: "model", name: "csrf-rejected", versionScheme: "integer" },
      403,
      { ...headers, "X-CSRF-Token": "invalid" },
    );
    const anonymous = await browser.newContext();
    try {
      check(
        (
          await anonymous.request.get(
            config.publicURL + "/artifact/v1/artifacts",
            { headers },
          )
        ).status() === 401,
        "AnonymousNotRejected",
      );
    } finally {
      await anonymous.close();
    }
    report.verifiedAt = new Date().toISOString();
    report.checks = [
      ...new Set([
        ...(report.checks || []),
        "real Casdoor login",
        "published file reference",
        "STS GET exact bytes",
        "old deleted file grant denied",
        "CSRF denied",
        "anonymous denied",
      ]),
    ];
    if (!process.argv.includes("--verify"))
      report.checks.push("STS PUT", "Complete ready", "file version publish");
    fs.writeFileSync(reportPath, JSON.stringify(report, null, 2) + "\n", {
      mode: 0o600,
    });
    await logout();
    logout = undefined;
    console.log(JSON.stringify({ status: "passed", ...report }));
  } finally {
    try {
      if (logout) await logout();
    } finally {
      try {
        if (browser) await browser.close();
      } finally {
        fs.rmSync(dir, { recursive: true, force: true });
      }
    }
  }
}
main().catch((error) => {
  console.error(
    JSON.stringify({
      stage,
      name: error.name,
      ...(error.status ? { status: error.status } : {}),
      ...(error.diagnostic ? { diagnostic: error.diagnostic } : {}),
    }),
  );
  process.exitCode = 1;
});
