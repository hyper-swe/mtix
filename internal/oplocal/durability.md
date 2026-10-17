# Operator-local write durability

This note describes the requests made by the atomic record writer. It does not claim that a successful API call proves survival of a power loss. Existing ownership, access, type and handle checks remain prerequisites.

| Platform | Before publication | After publication |
| --- | --- | --- |
| Darwin | `fcntl(F_FULLFSYNC)` on the complete temporary file | `F_FULLFSYNC` on the verified directory |
| Other Unix | File `Sync`, then close | Directory `Sync` |
| Windows | `FlushFileBuffers` on the verified writable file handle | Another `FlushFileBuffers` on that same handle after relative rename |

Darwin retries interrupted calls. Only `ENOTSUP` selects a raw `fsync` fallback and a structured warning; other errors propagate. Darwin `EOPNOTSUPP` is a different errno and does not select that fallback. The fallback does not call `File.Sync`, which could request full sync again. Apple describes full sync as requesting both filesystem sync and a device-cache flush, and documents hardware limits. [Apple fcntl documentation](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/fcntl.2.html)

Windows retains `FILE_WRITE_THROUGH` and handle-relative `NtSetInformationFile` publication. Its file handle has write access; the parent handle has read access and is not passed to `FlushFileBuffers`. Microsoft documents the native write-through option and the file flush access requirement. [NtCreateFile options](https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/ntifs/nf-ntifs-ntcreatefile), [FlushFileBuffers](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-flushfilebuffers)

Microsoft's Win32 write-through documentation includes NTFS metadata changes, including rename, but also limits hardware support. Here that is bounded context for the native write-through and post-rename file-flush requests, not proof of an independent parent-directory flush or an identical `MoveFileEx(MOVEFILE_WRITE_THROUGH)` contract. The approved implementation retains the relative-handle rename. Filesystem and storage-device behavior remain outside an application's ability to prove sudden-power-loss safety from return codes. [NTFS write-through metadata documentation](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createfilew)

## Failure and publication boundary

Before publication, a failure preserves the previous record and cleans only the owned temporary file. After publication, a flush error can leave complete new bytes visible. In particular, Windows marks the file as committed immediately after rename, before the final flush, so a failed final flush cannot discard the published record.

Native flush failures preserve their cause through a private write-error type without newly adding `ErrOperatorStateUnreadable`. Independent validation, read, close and cleanup errors retain their own classifications. `CreateJSON` propagates a marked flush error instead of treating readable new content as successful contention recovery; host-id initialization likewise propagates a marked failure. The historical injected-directory callback contract remains unchanged.

Operators must preserve records and surface the originating I/O error. Read-only `mtix hooks status --json` validates placement and access; it does not reverse a publication or establish durability. Multiple permission or alert records do not become one filesystem transaction through these flush calls. Deletion retains its existing platform behavior; this note's flush sequence describes atomic writes.

## Evidence and limits

Syscall-seam tests prove dispatch, retry, unsupported fallback, warnings and error propagation. Real Darwin tests record file and directory requests and native results. Windows runtime tests query actual write-through handle mode and filesystem, observe pre- and post-publication names, and execute both file flushes. The label-triggered Windows workflow requires actual RUN and PASS events, no unexpected skips, and retains raw events, coverage and runtime metadata. A cross-compile or the ordinary Linux/macOS CI matrix is not native Windows evidence. No test simulates sudden power loss or proves a device honors its cache-flush requests.
