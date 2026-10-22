// Real Casdoor login and STS acceptance. Credentials and session data stay in memory.
const fs = require("node:fs");
const path = require("node:path");
const os = require("node:os");
const crypto = require("node:crypto");
const assert = require("node:assert/strict");
const { spawnSync } = require("node:child_process");
const { verifyReferenceRelease, pollUntil } = require("./reference-release.cjs");
const root = path.resolve(__dirname, "../..");
const web = process.env.FORGE_WEB_ROOT || path.resolve(root, "../forge-tenant-web");
const reportPath = process.env.FORGE_REDEPLOY_REPORT;
const timeoutMs = 20 * 60 * 1000;
const data = "artifact-go durable OrbStack redeploy acceptance\n";
let stage = "prepare";
function check(value, name) {
  if (!value) throw Object.assign(new Error(), { name });
}
function id() { return crypto.randomUUID(); }

async function main() {
  check(reportPath, "ReportPathRequired");
  check(process.env.FORGE_CONFIG || process.env.FORGE_CONFIG_STDIN === "1", "ExplicitLoginConfigRequired");
  const config = JSON.parse(fs.readFileSync(process.env.FORGE_CONFIG_STDIN === "1" ? 0 : process.env.FORGE_CONFIG));
  check(process.env.FORGE_TEST_SCOPE || config.scope, "ExplicitScopeRequired");
  const configured = config.scope || JSON.parse(fs.readFileSync(process.env.FORGE_TEST_SCOPE));
  check(/^t_[A-Za-z0-9_-]{1,64}$/.test(configured.TenantID), "TenantScopeMustBeTenantID");
  check(/^p_[A-Za-z0-9_-]{1,64}$/.test(configured.ProjectID), "ProjectScopeMustBeProjectID");
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "artifact-transfer-"));
  fs.chmodSync(dir, 0o700);
  const binary = path.join(dir, "transfer");
  let browser, logout;
  try {
    stage = "build transfer";
    const build = spawnSync("go", ["build", "-o", binary, "deploy/acceptance/forge-transfer/main.go"], { cwd: root, encoding: "utf8", timeout: 120000 });
    check(build.status === 0, "TransferBuildFailed");
    const { chromium } = require(path.join(web, "node_modules/playwright"));
    browser = await chromium.launch({ executablePath: process.env.CHROME_EXECUTABLE || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", headless: true, args: ["--no-proxy-server"] });
    const context = await browser.newContext();
    context.setDefaultTimeout(30000);
    const page = await context.newPage();
    const login = [];
    page.on("response", (r) => { const p = new URL(r.url()).pathname; if (["/api/login", "/auth/callback"].includes(p)) login.push([p, r.status()]); });
    async function restartArtifact() {
      stage = "restart deployment";
      const deployment = process.env.FORGE_ARTIFACT_DEPLOYMENT || "forge-artifact";
      const namespace = process.env.FORGE_NAMESPACE || "forge";
      const kube = ["--context", process.env.FORGE_KUBE_CONTEXT || "orbstack", "-n", namespace];
      const spec = spawnSync("kubectl", [...kube, "get", `deployment/${deployment}`, "-o", "json"], { encoding: "utf8", timeout: 30000 });
      check(spec.status === 0, "DeploymentReadFailed");
      const selector = Object.entries(JSON.parse(spec.stdout).spec.selector.matchLabels).map(([key, value]) => `${key}=${value}`).join(",");
      check(selector, "DeploymentSelectorRequired");
      const pods = spawnSync("kubectl", [...kube, "get", "pods", "-l", selector, "-o", "name"], { encoding: "utf8", timeout: 30000 });
      check(pods.status === 0 && pods.stdout.trim(), "DeploymentPodsRequired");
      const oldPods = pods.stdout.trim().split(/\s+/);
      for (const args of [["rollout", "restart", `deployment/${deployment}`], ["rollout", "status", `deployment/${deployment}`, "--timeout=180s"], ["wait", "--for=delete", ...oldPods, "--timeout=90s"]]) {
        const result = spawnSync("kubectl", [...kube, ...args], { encoding: "utf8", timeout: 190000 });
        check(result.status === 0, "RestartFailed");
      }
      console.log(JSON.stringify({ stage, readyAt: new Date().toISOString() }));
    }
    stage = "Casdoor login";
    await page.goto(config.publicURL);
    await page.locator("#username").fill(config.adminUsername);
    await page.locator("#password").fill(config.adminPassword);
    await page.locator("#submit").click();
    await page.waitForURL((u) => u.pathname === "/");
    check(login.some(([p, s]) => p === "/api/login" && s === 200) && login.some(([p, s]) => p === "/auth/callback" && s === 303), "RealLoginCallbackRequired");
    const meResponse = await context.request.get(config.publicURL + "/api/v1/me");
    check(meResponse.status() === 200, "MeFailed");
    const me = await meResponse.json();
    const userID = me.user && me.user.id;
    check(typeof userID === "string" && userID.length > 0, "RealMeUserIDRequired");
    logout = async () => {
      const r = await context.request.post(config.publicURL + "/auth/logout", { headers: { Origin: config.publicURL, "X-CSRF-Token": me.csrf_token } });
      check(r.status() === 200, "LogoutFailed");
    };
    const headersFor = (scope) => ({ "x-forge-tenant-id": scope.tenantID, ...(scope.projectID ? { "x-forge-project-id": scope.projectID } : {}), ...(scope.userID ? { "x-resource-user-id": scope.userID } : {}) });
    async function request(method, url, body, expected = 200, headers = {}) {
      stage = method + " " + url;
      const result = await page.evaluate(async ({ method, url, body, headers, csrf }) => {
        const started = performance.now();
        try {
          const r = await fetch(url, { method, signal: AbortSignal.timeout(30000), headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf, ...headers }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
          let payload;
          try { payload = await r.json(); } catch { payload = null; }
          return { status: r.status, body: payload, durationMs: Math.round(performance.now() - started) };
        } catch (e) {
          return { error: ["TimeoutError", "AbortError", "TypeError", "SyntaxError"].includes(e.name) ? e.name : "RequestError", durationMs: Math.round(performance.now() - started) };
        }
      }, { method, url, body, headers, csrf: me.csrf_token });
      if (process.env.FORGE_DIAGNOSTICS === "1") console.log(JSON.stringify({ stage, status: result.status, durationMs: result.durationMs }));
      if (result.error || result.status !== expected) throw Object.assign(new Error(), { name: result.error || "HTTPStatusMismatch", status: result.status });
      return result.body;
    }
    async function transfer(operation, grant) {
      // A restarted Pod can be ready before the local port-forward reconnects.
      await pollUntil(async () => {
        try {
          const response = await fetch(grant.endpoint, { signal: AbortSignal.timeout(2000) });
          await response.body?.cancel();
          return response.status;
        } catch { return 0; }
      }, (status) => status === 403, "S3EndpointUnavailable", { timeoutMs: 30000, intervalMs: 1000 });
      stage = "STS " + operation;
      const result = spawnSync(binary, [], { input: JSON.stringify({ operation, grant, data }), encoding: "utf8", timeout: 40000 });
      check(result.status === 0, "STSTransferFailed");
    }
    const grant = (fileID, scope, action) => request("POST", "/storage/v1/storage/grants", { fileIDs: [fileID], scope: { actions: [action] } }, 200, headersFor(scope));
    async function upload(scope, key = id(), onGrant) {
      const opts = headersFor(scope);
      const items = await request("POST", "/storage/v1/storage/files", [{ requestKey: key, size: Buffer.byteLength(data), contentType: "text/plain" }], 200, opts);
      check(items.length === 1 && !items[0].error && items[0].fileID, "FileRegistrationFailed");
      const fileID = items[0].fileID;
      if (key) {
        const replay = await request("POST", "/storage/v1/storage/files", [{ requestKey: key, size: Buffer.byteLength(data), contentType: "text/plain" }], 200, opts);
        check(replay[0].fileID === fileID && !replay[0].error, "FileRequestReplayFailed");
        const conflict = await request("POST", "/storage/v1/storage/files", [{ requestKey: key, size: Buffer.byteLength(data) + 1, contentType: "text/plain" }], 200, opts);
        check(Boolean(conflict[0].error), "FileRequestParameterConflictRequired");
      }
      const uploadGrant = await grant(fileID, scope, "s3:PutObject");
      if (onGrant) onGrant(uploadGrant.expiresAt);
      await transfer("put", uploadGrant);
      const complete = await request("POST", "/storage/v1/storage/files/complete", [fileID], 200, opts);
      check(complete.length === 1 && !complete[0].error && complete[0].state === "ready", "FileCompleteFailed");
      return fileID;
    }
    async function publish(scope, fileID, name, draft = false) {
      const h = headersFor(scope);
      const artifact = await request("POST", "/artifact/v1/artifacts", { type: "model", name, versionScheme: "integer", requestKey: id() }, 201, h);
      await request("POST", "/artifact/v1/artifacts", { type: "model", name, versionScheme: "integer", requestKey: id() }, 409, h);
      let version;
      if (draft) {
        const d = await request("POST", `/artifact/v1/artifacts/${artifact.id}/drafts`, { requestKey: id(), payload: { acceptance: "personal-release" }, files: [{ fileID, path: "acceptance.txt" }] }, 201, h);
        version = await request("POST", `/artifact/v1/artifacts/${artifact.id}/versions`, { requestKey: id(), draftID: d.id, expectedDraftRevision: d.revision }, 201, h);
      } else {
        version = await request("POST", `/artifact/v1/artifacts/${artifact.id}/versions`, { requestKey: id(), payload: { acceptance: "durable-redeploy" }, files: [{ fileID, path: "acceptance.txt" }] }, 201, h);
      }
      check(version && version.id && version.files.some((f) => f.fileID === fileID), "VersionPublishFailed");
      const versionPath = `/artifact/v1/artifacts/${artifact.id}/versions/${version.id}`;
      const got = await request("GET", versionPath, undefined, 200, h);
      check(got.state === "ready" && got.files.some((f) => f.fileID === fileID), "PublishedFileReferenceMissing");
      const resolved = await request("POST", "/artifact/v1/resolve", { uri: version.versionUri }, 200, h);
      check(resolved.version && resolved.version.id === version.id && resolved.version.files.some((file) => file.fileID === fileID) && !resolved.file, "ResolveMismatch");
      await transfer("get", await grant(fileID, scope, "s3:GetObject"));
      return { artifactID: artifact.id, versionID: version.id, versionURI: version.versionUri, fileID };
    }
    let report;
    const saveReport = () => fs.writeFileSync(reportPath, JSON.stringify(report, null, 2) + "\n", { mode: 0o600 });
    if (process.argv.includes("--verify")) {
      report = JSON.parse(fs.readFileSync(reportPath));
      assert.deepEqual(report.configuredScope, configured);
      check(report.phase === "created" && report.entries?.length === 4 && report.release && report.uploadGrant, "IncompleteReportCannotBeVerified");
      check(report.userID === userID, "ReportOwnerMismatch");
    } else {
      check(!fs.existsSync(reportPath), "ReportAlreadyExistsUseVerify");
      // Record identity before creating anything so a failed run leaves traceable evidence.
      report = { format: 2, phase: "creating", configuredScope: configured, userID, createdAt: new Date().toISOString() };
      saveReport();
      const scopes = [
        { name: "tenant-shared", tenantID: configured.TenantID },
        { name: "tenant-personal", tenantID: configured.TenantID, userID },
        { name: "project-shared", tenantID: configured.TenantID, projectID: configured.ProjectID },
        { name: "project-personal", tenantID: configured.TenantID, projectID: configured.ProjectID, userID },
      ];
      report.entries = [];
      for (const s of scopes) {
        const fileID = await upload(s);
        report.entries.push({ scope: s, ...(await publish(s, fileID, s.name + "-" + id(), s.name === "project-personal")) });
        saveReport();
      }
      // Collection create requestKey replay/conflict/concurrency is covered in TestCollectionCreateIdempotency.
      const personal = report.entries.find((e) => e.scope.name === "project-personal");
      let initialGrantExpiry;
      const releaseFileID = await upload(personal.scope, id(), (expiry) => { initialGrantExpiry = expiry; });
      check(initialGrantExpiry, "InitialGrantExpiryRequired");
      const releaseArtifact = await request("POST", "/artifact/v1/artifacts", { type: "model", name: "release-" + id(), versionScheme: "integer", requestKey: id() }, 201, headersFor(personal.scope));
      const sharedRef = await request("POST", `/artifact/v1/artifacts/${releaseArtifact.id}/versions`, { requestKey: id(), files: [{ fileID: releaseFileID, path: "shared-ref.txt" }] }, 201, headersFor(personal.scope));
      const finalRef = await request("POST", `/artifact/v1/artifacts/${releaseArtifact.id}/versions`, { requestKey: id(), files: [{ fileID: releaseFileID, path: "final-ref.txt" }] }, 201, headersFor(personal.scope));
      report.release = { scope: personal.scope, artifactID: releaseArtifact.id, sharedVersionID: sharedRef.id, finalVersionID: finalRef.id, fileID: releaseFileID, initialGrantExpiresAt: initialGrantExpiry };
      saveReport();
      report.tombstoneSampleID = await upload(personal.scope, id(), (expiry) => {
        report.uploadGrant = { grantExpiresAt: expiry };
      });
      report.uploadGrant.fileID = report.tombstoneSampleID;
      saveReport();
      const deleted = await request("DELETE", `/storage/v1/storage/files/${report.tombstoneSampleID}`, undefined, 202, headersFor(personal.scope));
      check(deleted.state === "deleting" || deleted.state === "deleted", "DeleteSampleNotScheduled");
      report.phase = "created";
      saveReport();
    }

    if (process.argv.includes("--restart")) await restartArtifact();
    if (report.entries) {
      const personalScope = report.entries.find((e) => e.scope.name === "project-personal").scope;
      await pollUntil(
        () => request("GET", `/storage/v1/storage/files/${report.tombstoneSampleID}`, undefined, 200, headersFor(personalScope)),
        (sample) => {
          if (report.uploadGrant) {
            const expiry = Date.parse(report.uploadGrant.grantExpiresAt);
            check(Number.isFinite(expiry), "GrantExpiryRequired");
            if (Date.now() < expiry) {
              check(sample.state === "deleting", "UploadGrantReclamationWasEarly");
              if (!report.uploadGrant.observedProtectedAt) {
                report.uploadGrant.observedProtectedAt = new Date().toISOString();
                saveReport();
              }
            }
          }
          return sample.state === "deleted";
        },
        "DeleteSampleWorkerNotCompleted",
        { timeoutMs },
      );
      if (report.uploadGrant) {
        check(report.uploadGrant.observedProtectedAt, "UploadGrantProtectionNotObserved");
        report.uploadGrant.observedDeletedAt ||= new Date().toISOString();
        saveReport();
      }
      await request("POST", "/storage/v1/storage/grants", { fileIDs: [report.tombstoneSampleID], scope: { actions: ["s3:GetObject"] } }, 409, headersFor(personalScope));
      for (const entry of report.entries) {
        const h = headersFor(entry.scope);
        const got = await request("GET", `/artifact/v1/artifacts/${entry.artifactID}/versions/${entry.versionID}`, undefined, 200, h);
        check(got.state === "ready" && got.files.some((f) => f.fileID === entry.fileID), "VersionReadFailed");
        const resolved = await request("POST", "/artifact/v1/resolve", { uri: entry.versionURI }, 200, h);
        check(resolved.version && resolved.version.id === entry.versionID && resolved.version.files.some((file) => file.fileID === entry.fileID) && !resolved.file, "ResolveIsolationFailed");
        await transfer("get", await grant(entry.fileID, entry.scope, "s3:GetObject"));
        const wrong = report.entries.find((other) => other.scope.name !== entry.scope.name);
        await request("GET", `/storage/v1/storage/files/${entry.fileID}`, undefined, 404, headersFor(wrong.scope));
        const complete = await request("POST", "/storage/v1/storage/files/complete", [entry.fileID], 200, headersFor(wrong.scope));
        check(complete.length === 1 && complete[0].error && complete[0].error.status === 404 && complete[0].error.code === "FileNotFound", "CrossScopeCompleteMustFail");
        await request("POST", "/storage/v1/storage/grants", { fileIDs: [entry.fileID], scope: { actions: ["s3:GetObject"] } }, 404, headersFor(wrong.scope));
        await request("DELETE", `/storage/v1/storage/files/${entry.fileID}`, undefined, 404, headersFor(wrong.scope));
      }
      // Wrong-scope artifact, version, draft and resolver reads must not disclose any entry.
      for (const entry of report.entries) {
        const wrong = report.entries.find((other) => other.scope.name !== entry.scope.name);
        const h = headersFor(wrong.scope);
        await request("GET", `/artifact/v1/artifacts/${entry.artifactID}`, undefined, 404, h);
        await request("GET", `/artifact/v1/artifacts/${entry.artifactID}/versions/${entry.versionID}`, undefined, 404, h);
        await request("GET", `/artifact/v1/artifacts/${entry.artifactID}/drafts`, undefined, 404, h);
        await request("GET", `/artifact/v1/artifacts/${entry.artifactID}/aliases/latest`, undefined, 404, h);
        await request("GET", `/artifact/v1/artifacts/${entry.artifactID}/retention-policy`, undefined, 405, h);
        await request("POST", "/artifact/v1/resolve", { uri: entry.versionURI }, 404, h);
      }
      const release = report.release;
      if (release) {
        check(Number.isFinite(Date.parse(release.initialGrantExpiresAt)), "GrantExpiryRequired");
        stage = "personal reference release";
        await verifyReferenceRelease(release, {
          request,
          headers: headersFor(release.scope),
          readBytes: async (fileID) => transfer("get", await grant(fileID, release.scope, "s3:GetObject")),
          restart: restartArtifact,
          save: saveReport,
          poll: (read, done, label) => pollUntil(read, done, label, { timeoutMs }),
          // The upload grant on this file is the only write grant, and physical
          // deletion must wait for it to expire.
          grantExpiresAt: Date.parse(release.initialGrantExpiresAt),
        });
      }
    }

    await request("POST", "/artifact/v1/artifacts", { type: "model", name: "csrf-rejected-" + id(), versionScheme: "integer" }, 403, { ...headersFor({ tenantID: configured.TenantID, projectID: configured.ProjectID }), "X-CSRF-Token": "invalid" });
    const originCheck = await context.request.post(config.publicURL + "/artifact/v1/artifacts", { data: { type: "model", name: "origin-rejected-" + id(), versionScheme: "integer" }, headers: { Origin: "https://invalid.example", "X-CSRF-Token": me.csrf_token, ...headersFor({ tenantID: configured.TenantID, projectID: configured.ProjectID }) } });
    check(originCheck.status() === 403, "OriginMustBeRejected");
    const anonymous = await browser.newContext();
    try {
      const response = await anonymous.request.get(config.publicURL + "/artifact/v1/artifacts", { headers: headersFor({ tenantID: configured.TenantID, projectID: configured.ProjectID }) });
      check(response.status() === 401, "AnonymousNotRejected");
    } finally { await anonymous.close(); }
    report.verifiedAt = new Date().toISOString();
    report.checks = ["real Casdoor login", "real /me personal owner", "four-scope STS PUT/Complete/publish/Resolve/GET", "cross-scope isolation", "personal release after worker restart", "upload grant protection observed before expiry; deletion observed after expiry", "CSRF, Origin and anonymous rejection", "requestKey matrix covered by integration tests"];
    saveReport();
    await logout(); logout = undefined;
    console.log(JSON.stringify({ status: "passed", reportPath, publicIDs: report.entries ? report.entries.map((e) => ({ scope: e.scope.name, artifactID: e.artifactID, versionID: e.versionID, fileID: e.fileID })).concat([{ scope: "project-personal-release", fileID: report.release.fileID, artifactID: report.release.artifactID }]) : { artifactID: report.artifactID, versionID: report.versionID, fileID: report.fileID }, verifiedAt: report.verifiedAt }));
  } finally {
    try { if (logout) await logout(); } finally {
      try { if (browser) await browser.close(); } finally { fs.rmSync(dir, { recursive: true, force: true }); }
    }
  }
}
main().catch((error) => {
  console.error(JSON.stringify({ stage, name: error.name, ...(error.status ? { status: error.status } : {}) }));
  process.exitCode = 1;
});
