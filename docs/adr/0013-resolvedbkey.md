# ADR 0013. `.resolvedbkey` keys

**Status:** Accepted, pending PoC-05.

**Context.** Resolve imports `.resolvedbkey` keys (XML according to the 18 manual) exported by
Project Server [01 §8]. The format is undocumented and there are no free implementations.

**Decision.** Experimental module, disabled by default, only if PoC-05 shows that Resolve
accepts generated keys on Windows, macOS and Linux, and after legal review J-02. Manual
connection instructions are always the main path.

**Alternatives.** Never offering it (worse experience); offering it untested (risk of keys that
do not work or that leak the password).

**Consequences.** The key contains the password: downloading it requires reauthentication, is
recorded in the audit log, and the UI warns that it must be treated like a password.
