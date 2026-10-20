// Real Casdoor browser login and deployed Forge metadata acceptance.
// Requires the sibling web checkout's Playwright and private deployment.json.
const fs = require("node:fs");
const path = require("node:path");
const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const web =
  process.env.FORGE_WEB_ROOT ||
  path.resolve(__dirname, "../../../forge-tenant-web");
const { chromium } = require(path.join(web, "node_modules/playwright"));
const config = JSON.parse(
  fs.readFileSync(
    process.env.FORGE_CONFIG ||
      path.join(web, "../forge-tenant-api/.local/deployment.json"),
  ),
);
const stateDir = process.env.FORGE_ACCEPTANCE_DIR;
if (!stateDir || (fs.statSync(stateDir).mode & 0o777) !== 0o700)
  throw new Error(
    "FORGE_ACCEPTANCE_DIR must be an existing private 0700 directory",
  );
const phase = process.argv[2];
if (!["prepare", "verify"].includes(phase))
  throw new Error("usage: forge-login.cjs prepare|verify");
const scopeFile = path.join(stateDir, "acceptance-scope.json");
const base = config.publicURL;

(async () => {
  const browser = await chromium.launch({
    executablePath:
      process.env.CHROME_EXECUTABLE ||
      "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    headless: true,
    args: ["--no-proxy-server"],
  });
  try {
    const context = await browser.newContext();
    context.setDefaultTimeout(30000);
    const page = await context.newPage();
    const loginPaths = [];
    page.on("response", (response) => {
      const pathname = new URL(response.url()).pathname;
      if (["/api/login", "/auth/callback"].includes(pathname))
        loginPaths.push({ path: pathname, status: response.status() });
    });
    console.log("Opening login page");
    await page.goto(base);
    await page.locator("#username").fill(config.adminUsername);
    await page.locator("#password").fill(config.adminPassword);
    await page.locator("#submit").click();
    await page.waitForURL((url) => url.pathname === "/", { timeout: 30000 });
    console.log("Browser login callback completed");
    const meResponse = await context.request.get(base + "/api/v1/me");
    assert.equal(meResponse.status(), 200);
    const me = await meResponse.json();
    assert(loginPaths.some((r) => r.path === "/api/login" && r.status === 200));
    assert(
      loginPaths.some((r) => r.path === "/auth/callback" && r.status === 303),
    );
    // Requests run inside the logged-in browser: native same-origin cookies and Origin.
    async function request(method, url, data, expected = 200, headers = {}) {
      console.log(`${method} ${url}`);
      const response = await page.evaluate(
        async ({ method, url, data, headers, csrf }) => {
          const result = await fetch(url, {
            method,
            signal: AbortSignal.timeout(20000),
            headers: {
              "Content-Type": "application/json",
              "X-CSRF-Token": csrf,
              "Idempotency-Key": crypto.randomUUID(),
              ...headers,
            },
            ...(data === undefined ? {} : { body: JSON.stringify(data) }),
          });
          return { status: result.status, body: await result.json() };
        },
        { method, url, data, headers, csrf: me.csrf_token },
      );
      assert.equal(
        response.status,
        expected,
        `${method} ${url}: status ${response.status}, code ${response.body?.error?.code || response.body?.code || "unknown"}`,
      );
      return response.body;
    }
    if (phase === "prepare") {
      assert(!fs.existsSync(scopeFile), "scope already prepared; use verify");
      const slug = "artifact-check-" + Date.now().toString(36);
      await request(
        "POST",
        "/api/v1/platform/tenants",
        { name: "Artifact 登录联调", slug, admin_user_id: me.user.id },
        202,
      );
      const tenants = await request("GET", "/api/v1/tenants");
      const tenant = tenants.items.find((t) => t.slug === slug);
      assert(tenant);
      const snapshot = await request("GET", `/api/v1/tenants/${tenant.id}`);
      await request(
        "POST",
        `/api/v1/tenants/${tenant.id}/projects`,
        { name: "Artifact 接入验收", path: "artifact-check", mode: "create" },
        202,
        { "If-Match": String(snapshot.tenant.version) },
      );
      const updated = await request("GET", `/api/v1/tenants/${tenant.id}`);
      const project = updated.projects.find(
        (p) => p.name === "Artifact 接入验收",
      );
      assert(project);
      const scope = {
        tenantID: tenant.id,
        projectID: project.id,
        userID: me.user.id,
        slug,
      };
      fs.writeFileSync(scopeFile, JSON.stringify(scope), {
        mode: 0o600,
        flag: "wx",
      });
      console.log(JSON.stringify({ phase, loginPaths, ...scope }));
      return;
    }
    const scope = JSON.parse(fs.readFileSync(scopeFile));
    const checks = ["real Casdoor login and callback"];
    const tenantHeader = { "x-forge-tenant-id": scope.tenantID };
    const projectHeader = { ...tenantHeader, "x-forge-project-id": scope.projectID };
    const objects = [];
    for (const [name, headers] of [
      ["tenant", tenantHeader],
      ["project", projectHeader],
    ]) {
      const artifact = await request(
        "POST",
        "/artifact/v1/artifacts",
        {
          type: "model",
          name: "browser-" + name + "-" + crypto.randomUUID(),
          versionScheme: "integer",
        },
        201,
        headers,
      );
      assert.equal(artifact.createdBy, me.user.id);
      assert.equal(artifact.tenantID, scope.tenantID);
      assert.equal(
        artifact.projectID,
        name === "project" ? scope.projectID : null,
      );
      const url = `/artifact/v1/artifacts/${artifact.id}/versions`;
      const body = {
        requestKey: crypto.randomUUID(),
        payload: { verified: "real-browser-session", scope: name },
      };
      const version = await request("POST", url, body, 201, headers);
      const replay = await request("POST", url, body, 200, headers);
      assert.equal(replay.id, version.id);
      const read = await request(
        "GET",
        `${url}/${version.id}`,
        undefined,
        200,
        headers,
      );
      assert.deepEqual(read.payload, body.payload);
      await request(
        "GET",
        `${url}/${version.id}`,
        undefined,
        404,
        name === "tenant" ? projectHeader : tenantHeader,
      );
      objects.push({
        artifactID: artifact.id,
        versionID: version.id,
        scope: name,
      });
      checks.push(
        `${name} create/publish/replay/read and cross-scope rejection`,
      );
    }
    await request(
      "POST",
      "/artifact/v1/artifacts",
      { type: "model", name: "csrf-rejected", versionScheme: "integer" },
      403,
      { ...tenantHeader, "X-CSRF-Token": "invalid" },
    );
    const anonymous = await browser.newContext();
    const unauth = await anonymous.request.get(
      base + "/artifact/v1/artifacts",
      { headers: tenantHeader },
    );
    assert.equal(unauth.status(), 401);
    await anonymous.close();
    checks.push("CSRF 403 and anonymous 401");
    const memberTenants = (await request("GET", "/api/v1/tenants")).items;
    const allTenants = (await request("GET", "/api/v1/platform/tenants")).items;
    let nonmember = allTenants.find(
      (t) => t.status === "active" && !memberTenants.some((m) => m.id === t.id),
    );
    if (!nonmember) {
      const users = (await request("GET", "/api/v1/platform/users")).items;
      const other = users.find(
        (u) => u.id !== me.user.id && u.status === "active",
      );
      assert(
        other,
        "requires another active account for a nonmember negative control",
      );
      const slug = "artifact-denied-" + Date.now().toString(36);
      await request(
        "POST",
        "/api/v1/platform/tenants",
        { name: "Artifact 非成员拒绝验收", slug, admin_user_id: other.id },
        202,
      );
      nonmember = (await request("GET", "/api/v1/platform/tenants")).items.find(
        (t) => t.slug === slug,
      );
      assert(nonmember);
    }
    await request("GET", "/artifact/v1/artifacts", undefined, 403, {
      "x-forge-tenant-id": nonmember.id,
    });
    checks.push("operator without membership denied 403");
    const files = await request(
      "POST",
      "/storage/v1/storage/files",
      [{ requestKey: crypto.randomUUID(), size: 0, contentType: "text/plain" }],
      200,
      tenantHeader,
    );
    assert(
      !files[0].error,
      `bucket registration failed: ${files[0].error?.code}`,
    );
    assert.equal(files[0].state, "uploading");
    checks.push(
      "storage file registration after automatic bucket provisioning",
    );
    const report = {
      phase,
      loginPaths,
      scope,
      checks,
      objects,
      registeredFileID: files[0].fileID,
    };
    fs.writeFileSync(
      path.join(stateDir, "browser-acceptance.json"),
      JSON.stringify(report, null, 2),
      { mode: 0o600 },
    );
    console.log(JSON.stringify(report));
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error.message);
  process.exitCode = 1;
});
