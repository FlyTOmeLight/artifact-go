const assert = require("node:assert/strict");
const test = require("node:test");
const { verifyReferenceRelease, pollUntil } = require("./reference-release.cjs");

function fixture() {
  let time = 1000;
  let firstPolls = 0;
  const events = [];
  const release = { artifactID: "a", sharedVersionID: "first", finalVersionID: "last", fileID: "f" };
  const options = {
    headers: {}, now: () => time, grantExpiresAt: 0,
    save() { events.push(["save", structuredClone(release)]); },
    async restart() { events.push(["restart"]); },
    async readBytes() { events.push(["bytes"]); },
    poll: (read, done, label) => pollUntil(read, done, label, {
      timeoutMs: 100, intervalMs: 10, now: () => time,
      wait: async (ms) => { time += ms; },
    }),
    async request(method, url) {
      events.push([method, url]);
      if (method === "DELETE" && url.endsWith("/first")) {
        return { state: ++firstPolls < 3 ? "deleting" : "deleted" };
      }
      if (method === "GET" && url.endsWith("/last")) {
        assert.ok(firstPolls >= 3, "remaining bytes checked before release completed");
        return { state: "ready", files: [{ fileID: "f" }] };
      }
      if (method === "DELETE" && url.endsWith("/last")) {
        assert.ok(release.nonFinalVerifiedAt, "final reference removed before byte verification");
        return { state: "deleting" };
      }
      if (url === "/storage/v1/storage/files/f") return { state: "deleted" };
      throw new Error("unexpected request");
    },
  };
  return { release, options, events };
}

test("waits for release receipt and remaining bytes before removing last reference", async () => {
  const { release, options, events } = fixture();
  await verifyReferenceRelease(release, options);
  assert.equal(release.completed, true);
  assert.ok(release.observedDeletedAt);
  assert.equal(events.filter(([event]) => event === "bytes").length, 1);
});

test("failed restart is retried on resume and does not mark restart complete", async () => {
  const { release, options } = fixture();
  const restart = options.restart;
  options.restart = async () => { throw new Error("restart failed"); };
  await assert.rejects(verifyReferenceRelease(release, options), /restart failed/);
  assert.equal(release.started, true);
  assert.equal(release.restartedAt, undefined);
  assert.equal(release.nonFinalVerifiedAt, undefined);
  options.restart = restart;
  await verifyReferenceRelease(release, options);
  assert.equal(release.completed, true);
});

test("observed deletion before write grant expiry fails without completion evidence", async () => {
  const { release, options } = fixture();
  options.grantExpiresAt = 10000;
  await assert.rejects(verifyReferenceRelease(release, options), { name: "FileDeletedBeforeWriteGrantExpiry" });
  assert.equal(release.completed, undefined);
});

test("legacy completion without release evidence is rejected", async () => {
  const { release, options } = fixture();
  release.completed = true;
  await assert.rejects(verifyReferenceRelease(release, options), { name: "ReferenceReleaseEvidenceMissing" });
  assert.equal(release.nonFinalVerifiedAt, undefined);
});

test("completed release rechecks deletion after restart", async () => {
  const { release, options } = fixture();
  await verifyReferenceRelease(release, options);
  options.request = async () => ({ state: "bound" });
  await assert.rejects(verifyReferenceRelease(release, options), { name: "ReleasedFileNotDeleted" });
});

test("each poll invocation gets a fresh bounded deadline", async () => {
  let time = 0;
  const options = { timeoutMs: 20, intervalMs: 10, now: () => time, wait: async (ms) => { time += ms; } };
  await assert.rejects(pollUntil(async () => false, Boolean, "TimedOut", options), { name: "TimedOut" });
  assert.equal(time, 20);
  let reads = 0;
  await pollUntil(async () => ++reads === 2, Boolean, "TimedOut", options);
  assert.equal(time, 30);
});
