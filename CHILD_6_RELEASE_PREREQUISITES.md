# Child 6 (#134) — release signing and Play Console: what is done, and what needs you

**Short version.** Far more is done than #134 assumes. All five signing secrets
exist, the pipeline runs, and a signed release has already come out of it. What
is left is one engineering gap I am fixing in this PR, one irreversible thing you
should do today, and the Play Console work — which is calendar time, not
engineering time.

---

## 1. What is already done

#134 was written when none of this existed. Checked against the live repo on
**2026-09-03**:

- **All five repo secrets are set** (2026-09-02): `ANDROID_KEYSTORE_BASE64`,
  `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD`,
  `MOBILE_API_URL`.
- **The signing config exists** in `mobile/android/app/build.gradle`, and is
  written carefully: `signingConfigs.release` only binds a keystore when
  `rootProject.file('release.keystore')` actually exists, so a contributor with
  no keystore can still `assembleDebug` with no setup at all.
- **The pipeline works.** The `android-v0.0.1` tag produced a **successful** run
  of `.github/workflows/android.yml`.
- **A release exists**: _agentmesh 0.0.1_, correctly flagged **Pre-release**,
  with a signed `app-release.apk` (6.0 MB) attached.

So the honest status of #134's first acceptance criterion — "a tagged build
produces a signed `.aab`" — is **already met**.

---

## 2. The one engineering gap, which this PR fixes

`mobile/android/app/build.gradle` hardcodes:

```groovy
versionCode 1
versionName "1.0"
```

**Google Play rejects any upload whose `versionCode` is not strictly greater
than the last one accepted.** With it pinned at 1, your _first_ upload works and
your _second_ is impossible without somebody remembering to hand-edit this file
and commit it before every release. That is exactly the kind of step that gets
forgotten once and then costs an afternoon.

Note also that `versionName "1.0"` disagrees with the tag that produced the
build: the release is called _agentmesh 0.0.1_ while the APK inside reports
itself as 1.0.

This PR derives both from the git tag, so `android-v0.4.2` produces
`versionName 0.4.2` and a `versionCode` that always increases. Nothing to
remember, and nothing to edit per release.

---

## 3. Do this today, before anything else

> ### Back up the keystore, outside this repository.
>
> It currently exists in exactly one place: the `ANDROID_KEYSTORE_BASE64` GitHub
> secret. GitHub will not show it to you again.
>
> **If it is lost, the app can never be updated under the same Play listing
> again.** Not "it is difficult" — the listing is permanently frozen, and you
> would have to publish a new app under a new package name and ask every user to
> reinstall.
>
> Whoever created it on 2026-09-02 should still have the `.jks`/`.keystore` file.
> Put a copy — plus the store password, key alias and key password — in a
> password manager and somewhere offline. Do it before the Play listing exists,
> because after that the cost of losing it changes from "annoying" to
> "unrecoverable".

---

## 4. What only you can do: Play Console

None of this can be done from inside the repository, and none of it is
engineering work.

### 4.1 Create the app and upload a build

The `.aab` is **not** attached to the GitHub Release — only the APK is. Play
wants the `.aab`, and it lives in the workflow run's **artifacts**
(`agentmesh-release-aab`), which have a **7-day retention**. If the 0.0.1
artifacts have expired, re-run the workflow or cut a new tag.

Upload it to the **internal testing** track. Publishing is deliberately manual —
a release should be a decision somebody makes, not a side effect of pushing a
tag.

### 4.2 Privacy policy URL — mandatory

Required the moment background location is declared, and Play checks that the
URL resolves.

**There was no privacy policy anywhere in this project** — no page, no route, no
external link. This PR adds one at `/privacy`, drafted from what the app
actually does rather than from a template.

> **It needs your review before you publish it.** I have described the app's real
> behaviour accurately, but a privacy policy is a binding legal document and Play
> will hold you to what it says. Read it, and have someone qualified read it if
> this is going in front of a real audience.

The URL to give Play will be `https://www.agent-mesh.app/privacy`.

### 4.3 Data Safety form

This must match the app's actual behaviour or the submission is rejected — and
being caught overstating or understating is worse than a slow review. Grounded
in the code, the answers are:

| Question                                   | Answer                                                             | Where this comes from                                                                               |
| ------------------------------------------ | ------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------- |
| Does the app collect location?             | **Yes — approximate and precise**                                  | The geofence trigger                                                                                |
| Is location **shared** with third parties? | **No**                                                             | Fixes go only to our own backend                                                                    |
| Is location **stored** on your servers?    | **No**                                                             | Only the derived `geofence_inside` boolean and a timestamp (migration `000029`). Never coordinates. |
| Is it processed ephemerally?               | **Yes**                                                            | A fix answers "did you cross the edge", then is discarded                                           |
| Is collection required to use the app?     | **No**                                                             | Everything else works without it; the feature simply stays off                                      |
| Can users request deletion?                | **Yes**                                                            | Clearing the zone removes the stored state                                                          |
| Encrypted in transit?                      | **Yes**                                                            | HTTPS                                                                                               |
| Other data collected                       | **Email address** (account) and **app interactions** (run history) |                                                                                                     |

One nuance worth stating honestly: **undelivered location fixes are held briefly
on the device** while offline, and deleted once sent or within a day
(`frontend/src/native/queue.ts`). That is on-device storage rather than
collection, but the in-app disclosure already says so and your form must not
contradict it.

### 4.4 Background-location declaration

`ACCESS_BACKGROUND_LOCATION` gets a **dedicated Google review**, separate from
the normal one. They will want:

- a written justification — "the app starts a user-configured workflow when the
  user crosses the boundary of a place they chose; that cannot work while the app
  is closed without background location";
- **a short video** of the feature in use, including the in-app disclosure
  appearing _before_ the system permission dialog. The app already does this
  correctly (`frontend/src/native/permissions.ts`), and it is the single biggest
  factor in these reviews — record it showing that sequence explicitly.

**Budget calendar time, not engineering time. Expect a resubmit.** This review
routinely takes one to three weeks, and rejections are more often about the video
than about the app.

### 4.5 Store listing

Screenshots (phone at minimum), short and full descriptions, an icon, a feature
graphic, and the content rating questionnaire.

---

## 5. What still cannot be verified

**"The `.aab` installs from the internal-testing track on a real device."** That
is #134's second acceptance criterion and it needs a physical phone. Same wall as
#128, which stays parked outside the chain.

Everything up to that point is either already done or in this PR.

---

## Summary

|                      |                                                                                                                          |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| **Already done**     | Five secrets · signing config · working pipeline · signed 0.0.1 pre-release                                              |
| **Fixed in this PR** | `versionCode`/`versionName` derived from the tag · a privacy policy page                                                 |
| **Do today**         | **Back up the keystore outside the repo — losing it is unrecoverable**                                                   |
| **Only you can do**  | Play Console: upload the `.aab`, privacy policy URL, Data Safety, background-location declaration + video, store listing |
| **Needs hardware**   | Installing from the internal-testing track (#128)                                                                        |
| **Budget**           | The background-location review is calendar time. Expect a resubmit.                                                      |
