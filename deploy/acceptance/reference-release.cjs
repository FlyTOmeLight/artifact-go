// Persist evidence only after the corresponding operation has succeeded.
function failure(name) {
  return Object.assign(new Error(name), { name });
}

async function verifyReferenceRelease(
  release,
  { request, headers, readBytes, restart, save, poll, grantExpiresAt, now = Date.now },
) {
  if (release.completed) {
    if (!release.restartedAt || !release.nonFinalVerifiedAt || !release.observedDeletedAt) {
      throw failure("ReferenceReleaseEvidenceMissing");
    }
    const file = await request("GET", `/storage/v1/storage/files/${release.fileID}`, undefined, 200, headers);
    if (file.state !== "deleted") throw failure("ReleasedFileNotDeleted");
    return;
  }
  const version = (id) => `/artifact/v1/artifacts/${release.artifactID}/versions/${id}`;
  const remove = async (id) => {
    const result = await request("DELETE", version(id), undefined, 200, headers);
    if (!["deleting", "deleted"].includes(result.state)) throw failure("DeleteVersionMustQueueRelease");
    return result;
  };
  if (!release.started) {
    await remove(release.sharedVersionID);
    release.started = true;
    save();
  }
  if (!release.restartedAt) {
    await restart();
    release.restartedAt = new Date(now()).toISOString();
    save();
  }
  if (!release.nonFinalVerifiedAt) {
    // A deleted version receipt means its reference-release task completed.
    // File state 'bound' alone cannot prove that task has run.
    await poll(
      () => remove(release.sharedVersionID),
      (result) => result.state === "deleted",
      "FirstReferenceReleaseTimedOut",
    );
    const remaining = await request("GET", version(release.finalVersionID), undefined, 200, headers);
    if (
      remaining.state !== "ready" ||
      !remaining.files.some((file) => file.fileID === release.fileID)
    ) {
      throw failure("NonFinalReferenceLost");
    }
    await readBytes(release.fileID);
    release.nonFinalVerifiedAt = new Date(now()).toISOString();
    save();
  }
  await remove(release.finalVersionID);
  await poll(
    () => request("GET", `/storage/v1/storage/files/${release.fileID}`, undefined, 200, headers),
    (result) => {
      if (result.state !== "deleted") return false;
      // Physical deletion must wait for the last write grant to expire.
      if (now() < grantExpiresAt) throw failure("FileDeletedBeforeWriteGrantExpiry");
      return true;
    },
    "LastReferenceReleaseTimedOut",
  );
  release.completed = true;
  // This is an observation time, never the server's physical deletion time.
  release.observedDeletedAt = new Date(now()).toISOString();
  save();
}

async function pollUntil(
  read,
  done,
  label,
  {
    deadlineMs,
    timeoutMs,
    intervalMs = 5000,
    now = Date.now,
    wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  },
) {
  const deadline = deadlineMs ?? now() + timeoutMs;
  for (;;) {
    const result = await read();
    if (done(result)) return result;
    if (now() >= deadline) throw failure(label);
    await wait(Math.min(intervalMs, deadline - now()));
  }
}

module.exports = { verifyReferenceRelease, pollUntil };
