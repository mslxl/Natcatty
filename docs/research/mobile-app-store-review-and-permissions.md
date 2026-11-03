# Mobile App Store review, permissions, and networking constraints

Research ticket: Mobile App Store review, permissions, and networking constraints for Natcatty as an SSH/mosh/ET client built on MyGo PR 940 (`egoist/mygo#940`, branch `mobile`, head `7153b6c6`).

Date: 2026-11-04. This is engineering and risk research, not legal advice. Export-control and App Review determinations marked "needs counsel" must be confirmed by qualified counsel and, for the US EAR, by BIS.

## Answers

1. **iOS App Store review is possible for an SSH/mosh/ET client, but only as a self-contained, public-API app.** Guideline 2.5.2 forbids downloading, installing, or executing code that changes the app; an SSH client executes commands on the user's remote server, not code inside the app. Shipping App Store apps prove the category is accepted: Prompt 3 supports SSH, Mosh, and Eternal Terminal; Blink supports SSH and Mosh; Termius supports SSH and Mosh; iSH ships a local Linux-like shell [S1][S38][S39][S40][S42]. Do not download mosh/ET binaries, plugins, or scripts at runtime. [S1]

2. **Local network access on iOS is a user permission, not an entitlement.** The first outgoing TCP connection or UDP send to a private/local address triggers the system alert; until the user grants access, all local network communication is blocked, and the operation may fail immediately before the user answers. There is no general preflight API. Add `NSLocalNetworkUsageDescription` and retry after the prompt. Multicast entitlement is not needed for unicast SSH/mosh/ET; it is needed only for multicast/broadcast or arbitrary Bonjour service types. [S2][S3][S4][S6]

3. **Export compliance is mandatory; MyGo's default is a declaration, not a legal conclusion.** MyGo emits `ITSAppUsesNonExemptEncryption: false` for every app [S37]. SSH/mosh/ET use standard cryptography implemented in Go/SSH, not Apple OS cryptography. Apple's reference treats "industry standard algorithm, not provided within the Apple operating system" as requiring a French encryption declaration only when distributing in France; proprietary/non-standard algorithms require a CCATS [S10]. Separately, the US EAR "mass market" note normally allows self-classification as ECCN 5D992/5A992, and 15 CFR 740.17(e)(3) sets an annual self-classification report for qualifying mass-market encryption software, due February 1 to BIS and the ENC Encryption Request Coordinator [S21][S22]. The app team, with counsel, must determine whether the key can be `NO` (no non-exempt encryption under the applicable rules) or must be `YES` (non-exempt), and whether the mass-market report applies. Do not treat MyGo's default as the answer. [S8][S10][S12][S21][S22]

4. **No iOS background mode can keep a live terminal session alive.** App Review 2.5.4 allows background services only for their intended purposes [S1]. The `audio` mode is for audible content, and `voip` is for VoIP services [S13]; using either to keep an SSH socket alive is misuse and risks rejection. `BGTaskScheduler` tasks are short, system-scheduled, and for periodic work, not persistent connections: `BGAppRefreshTask` is short-duration; `BGProcessingTask` can run for minutes but only when the device is idle and is terminated when the user starts using the device [S15][S16][S17][S18]. Termius documents that iOS stops background activity in about 20-30 seconds and recommends `tmux`/`screen` [S41]. Product policy belongs to the "Define mobile session lifetime and background behavior" ticket; the review conclusion is that the app must reconnect, not claim background persistence.

5. **Android is currently safe only because MyGo hardcodes `targetSdk 36`.** MyGo's generated manifest declares `INTERNET`, `ACCESS_NETWORK_STATE`, and `POST_NOTIFICATIONS`; its Gradle shell hardcodes `compileSdk = 36`, `targetSdk = 36`, `minSdk = 26` [S37]. At target SDK 36, Android grants local-network access implicitly through `INTERNET`; Android says not to add `ACCESS_LOCAL_NETWORK` or request it at runtime for lower target SDKs [S24]. At target SDK 37 (Android 17), local network is blocked by default and the app must declare and request `ACCESS_LOCAL_NETWORK`, a dangerous permission in the `NEARBY_DEVICES` group [S24][S25]. Google Play requires new apps and updates to target API 36 from August 31, 2026 (extension to November 1, 2026); MyGo is compliant today, but the next target-level cycle will force a MyGo change [S23].

6. **Android foreground services are the main Play review risk.** Android 14+ requires a declared foreground-service type and type-specific permission, and Play requires a Play Console declaration with description, user impact, demo video, and use case [S26][S32]. There is no SSH/terminal foreground-service type. `dataSync` is capped at 6 hours in any 24-hour period for target SDK 35+; `specialUse` requires Play review and a manifest `<property>`; `connectedDevice` is described for external devices and has runtime prerequisites [S26][S27]. Doze still suspends network access during idle periods; a foreground service prevents App Standby idling but is not a blanket Doze exemption [S28][S29].

7. **What MyGo PR 940 already provides, versus app-side work.** MyGo provides iOS/Android hosts, Info.plist and AndroidManifest generation, signing, a generated privacy manifest, a background plugin (`BGTaskScheduler`/WorkManager), a network status API (`Online`, `Metered` only), and state save/restore [S37]. The app must add: the release `NSLocalNetworkUsageDescription` (MyGo only adds it to device debug builds), the export decision, Bonjour/ATS keys if used, foreground-service permissions/components via a plugin, the Android 17 local-network runtime request via a custom plugin, Play/ASC declarations, demo credentials, and a reconnect strategy. MyGo has no local-network permission API and no foreground-service abstraction. [S37]

8. **mosh/ET are not disallowed because they are non-standard protocols.** Mosh's State Synchronization Protocol runs over UDP (default ports 60000-61000) with AES-128-OCB3; ET uses SSH for authentication/encryption and a reconnectable TCP connection on port 2022 [S44][S45]. App Review guidelines and Play policies do not ban UDP or custom protocols. The real risks are local-network permission for LAN UDP, IPv6-only review (2.5.5), inability to test without a demo server running `mosh-server`/`et`, background suspension, and bundling all native code rather than downloading it. [S1][S2][S24][S33]

## 1. iOS App Store review

### 1.1 Guidelines that matter

Apple's App Store Review Guidelines were last updated October 16, 2026 [S1].

| Guideline | Text / effect for this app |
|---|---|
| 2.5.1 | Only public APIs, current OS, APIs for intended purposes. MyGo rejects builds that name Apple private selectors [S1][S37]. |
| 2.5.2 | App must be self-contained in its bundle and may not read or write data outside its container; may not download, install, or execute code that introduces or changes features or functionality, including other apps. Bundled Go/native libraries are fine; runtime downloads are not [S1]. |
| 2.5.4 | Multitasking apps may use background services only for their intended purposes: VoIP, audio playback, location, task completion, local notifications, etc. Do not use `audio` or `voip` to keep a terminal session alive [S1][S13]. |
| 2.5.5 | Apps must be fully functional on IPv6-only networks. Test SSH, mosh, and ET over IPv6 [S1]. |
| 2.1 | App completeness: provide an active demo account or fully featured demo mode, plus any resources needed to review; demo-account info is required if the app has a login [S1]. |
| 5.1.1 | Privacy policy link, consent for data collection, clear purpose strings, data minimization. Do not log or discover passwords surreptitiously [S1]. |
| 2.5.14 | If the app records, logs, or otherwise makes a record of user activity, request explicit consent and provide a clear visual and/or audible indication. Relevant if session logging is a feature [S1]. |

**2.5.2 and SSH.** The guideline is aimed at code that changes the app, not at commands a user runs on a remote host. Prompt 3's App Store description explicitly lists SSH, Mosh, and Eternal Terminal, which is direct evidence that Apple has approved an app in exactly this category [S38]. Blink, Termius, and iSH are additional approved precedents [S39][S40][S42]. This is a reasoned interpretation, not a guarantee: an app that also offers a local shell, script interpreter, or in-app package manager that downloads executables moves closer to 2.5.2 and Play's dynamic-code rule [S1][S33].

### 1.2 Shipping SSH/terminal precedents

| App | App Store metadata | Relevance |
|---|---|---|
| Prompt 3 (Panic) | Version 3.5.3; description: "Supporting SSH, Mosh, Eternal Terminal, Telnet, and Local Shells (on Mac)" [S38] | Strongest ET precedent: a shipping App Store app with SSH, mosh, and ET. |
| Blink Shell, Build & Code | Version 18.7.1; description: "leverages the support of Mosh and SSH"; "Mosh connections that even survive a device reboot" [S39] | Long-standing App Store terminal; mosh roaming is server-side persistence/reconnect, not iOS background execution. |
| Termius | Version 8.0.7; description: SSH, SFTP, Mosh, Telnet, port forwarding, serial, proxy/jump [S40] | Shipping cross-platform SSH client; Termius's own FAQ documents iOS background suspension in 20-30 seconds [S41]. |
| iSH Shell | Version 1.3.2; "A Linux-like shell on iOS" [S42] | Shows even a local Linux-like shell can be approved, but do not treat it as permission to download executable code at runtime. |
| ConnectBot (Google Play) | "ConnectBot: SSH & Mosh client"; supports SSH or Mosh [S43] | Android precedent that mosh/UDP is acceptable on Play. |
| JuiceSSH (Google Play) | Mosh support added August 9, 2013, per the mosh project's own timeline [S44] | Long-standing Play precedent for mosh. |

The precedents support the category, not every implementation choice. None of them makes silent-audio keepalive, background VoIP, or runtime code downloads compliant.

### 1.3 Apple local network privacy

#### What counts as local network

Apple's TN3179 (revision 2026-10-06) defines a local network as an IP network associated with a broadcast-capable interface, such as Wi-Fi or Ethernet, but not cellular (WWAN) or VPN. A local network address is any address on that network; traffic to it goes directly rather than through a router. All multicast addresses (`224.0.0.0/4`, `ff00::/8`) and the IPv4 broadcast address are local network addresses. [S2]

The operation table is decisive for an SSH/mosh/ET client:

| Operation | Local network access required |
|---|---|
| Making an outgoing TCP connection | Yes |
| Listening for and accepting incoming TCP connections | No |
| Sending a UDP unicast | Yes |
| Connecting a UDP socket | Yes |
| Sending/receiving UDP multicast or broadcast | Yes |
| Receiving an incoming UDP unicast | No |
| Resolving a local DNS name (`.local`) | Yes |
| Resolving a non-local DNS name with the system resolver | No |

These checks are implemented deep in the networking stack and apply to all networking APIs, including BSD Sockets, `Network.framework`, and anything built on them [S2]. Go's `net.Dial` to a private IPv4 address on Wi-Fi, or a mosh UDP socket to such an address, therefore triggers local network privacy. A connection to a public IP that happens to be reached through the LAN router is not a local network address and does not trigger the prompt. [S2]

#### First connect to a private address

The system presents the local network alert the first time the app performs a local network operation. It records the choice; later operations do not prompt. Your app starts in the undetermined state. [S2][S6]

Important behaviors:

- Until permission is granted, **all local network communication is blocked** [S6].
- When the alert is presented in response to an operation, the system **may deny that operation immediately, before the user responds**. Use an API that waits for connectivity, such as `NWConnection`, or add retry logic for BSD sockets [S2].
- If the app is in the background and its privilege is still undetermined, the system **denies the operation without presenting the alert**, and does not record a decision. A later foreground attempt presents the alert as if it were the first [S2].
- If the user denies, local network traffic is blocked. If the user changes an existing grant to deny, existing TCP connections close; new connections wait with `unsatisfiedReason == .localNetworkDenied` (or Bonjour `kDNSServiceErr_PolicyDenied`) and are automatically retried if the user later grants access [S2].
- There is **no general API to check local network access** and **no API to explicitly present the alert** (Apple bug FB8711182). A best-effort way to trigger it is to connect a UDP socket to a local network address (for example, a link-local address); this generates no traffic [S2].
- The simulator does not support local network privacy; test on a real device [S2].
- The alert text includes the usage description from `NSLocalNetworkUsageDescription`; WWDC20's transcript says apps that have not updated to the iOS 14 SDK show default reason text [S6].

#### Info.plist keys

`NSLocalNetworkUsageDescription`: Apple says any app that uses the local network, directly or indirectly, should include this description, including direct unicast or multicast connections to local hosts [S3]. The purpose string is the user-facing explanation in the prompt and should be specific; 5.1.1(ii) requires purpose strings to clearly and completely describe the use of data [S1].

`NSBonjourServices`: required if the app browses or advertises Bonjour services; it is an array of service types such as `_ipp._tcp` [S2][S5]. Not needed for direct host/port entry. If the app adds LAN host discovery over mDNS, list the service types.

`NSAppTransportSecurity` / `NSAllowsLocalNetworking`: ATS governs the URL Loading System, not raw TCP/UDP sockets. `NSAllowsLocalNetworking` permits ATS connections to unqualified domains, `.local` domains, and IP addresses for HTTP(S); it is not the local network privacy permission [S7]. Raw SSH/mosh/ET sockets are unaffected by ATS. If Natcatty's sync uses HTTP to a LAN address, check ATS separately; MyGo adds `NSAllowsLocalNetworking` only to device debug builds [S37].

#### Multicast entitlement

`com.apple.developer.networking.multicast` is required to send or receive IP multicast or broadcast on iOS and to browse or advertise arbitrary Bonjour service types; it requires permission from Apple [S4]. It is **not** required for unicast TCP or UDP, so SSH, mosh, and ET do not need it [S2]. Standard Bonjour service-type browsing requires local network access and `NSBonjourServices`, not the multicast entitlement; arbitrary service types or browsing all advertised types additionally require the entitlement [S2].

If the app later adds raw multicast discovery, it needs the entitlement plus an Apple-approved provisioning profile. MyGo can express entitlements only through a plugin (`pluginIOS.entitlements`), and signing fails unless the provisioning profile already contains the entitlement [S37].

#### Denial UX

Because there is no preflight API, the app should:

1. Attempt the connection in the foreground, so the prompt can appear.
2. Treat failure as possibly "permission not yet decided" rather than a hard network error, and retry after a short delay.
3. If the user denies, show an explanation and a path to Settings > Privacy & Security > Local Network; iOS does not let the app re-prompt.
4. Remember that a background attempt while undetermined fails silently and is not recorded; retry in the foreground.

### 1.4 Export compliance for SSH/mosh/ET cryptography

#### Apple's flow

App Store Connect asks export-compliance questions on every version unless the app's Info.plist answers them. `ITSAppUsesNonExemptEncryption` is a Boolean: `NO` means the app uses no encryption, or only encryption exempt from export compliance requirements; `YES` means non-exempt encryption. If `YES`, the app typically also sets `ITSEncryptionExportComplianceCode` to the code Apple provides after approving the uploaded documentation. If the key is absent, App Store Connect walks through the questionnaire on every upload. [S8][S11][S12]

Apple's reference table for required documentation is:

| Encryption in use | Required documentation |
|---|---|
| Limited to encryption within the Apple operating system | No documentation in App Store Connect |
| Industry-standard algorithm, not provided within the Apple operating system | French encryption declaration in App Store Connect, only if distributing in France |
| Proprietary/non-standard algorithms not accepted by international standards bodies | CCATS plus French encryption declaration |

Apple's overview says apps using standard encryption algorithms, crypto functionality in Apple's OS, or proprietary/non-standard algorithms all require an export compliance determination. It warns that the developer is responsible for deciding whether a formal CCATS is required and for the consequences of an inaccurate exemption claim. [S9][S10]

#### How this applies to Natcatty

- SSH uses standard algorithms (for example AES-GCM, Curve25519, Ed25519, RSA/SHA-2). Mosh uses AES-128-OCB3. ET uses SSH for handshaking and encryption [S44][S45]. These are industry-standard, app-provided algorithms, not proprietary cryptography.
- Under Apple's table, that normally means no CCATS and no US documentation upload, but a French encryption declaration if the app is distributed in France [S10].
- Under the US EAR, the "mass market" note (Note 3 to Category 5 Part 2 in the current eCFR text) allows self-classification under ECCN 5A992/5D992; §742.15 says such items are then no longer subject to that section [S21]. But §740.17(e)(3) requires a self-classification report for mass-market encryption components and executable software exported during the calendar year, received by BIS and the ENC Encryption Request Coordinator no later than February 1 of the following year [S22].
- MyGo's default `ITSAppUsesNonExemptEncryption: false` [S37] is therefore not a substitute for the determination. If counsel concludes the app's crypto is non-exempt, the key must become `YES`; if any non-standard algorithm is added, plan for a CCATS and `ITSEncryptionExportComplianceCode`. The separate annual mass-market report may also apply.

**Needs counsel:** the `NO`/`YES` answer, whether a CCATS is required for any component, the mass-market self-classification, and the French declaration.

#### Google Play export

Google Play's export-compliance policy says US export laws apply because Google is a US company and a download outside the US is an export. It names encryption categories including publicly available, authentication, digital signature, mass market, and ancillary; says open-sourcing does not necessarily exempt the app; and says determining compliance is the developer's responsibility, pointing to BIS [S36]. The cited Play page does not describe a per-build encryption questionnaire like App Store Connect's. The app must still comply with EAR and file any required report; verify whether Play Console presents an export declaration during upload.

### 1.5 Background modes and BGTaskScheduler

Apple's background execution modes include `audio` (plays audible content), `voip` (VoIP services), `fetch` (new content at regular intervals), `processing` (scheduled tasks), `remote-notification`, `location`, and others [S13][S14]. Apple says to use background modes sparingly and only for apps that provide the indicated service [S13].

Guideline 2.5.4 says multitasking apps may use background services only for their intended purposes [S1]. Consequences:

- `audio` is for audible playback. Silent audio to keep a terminal socket alive is not its intended purpose and is a classic 2.5.4 rejection risk.
- `voip` is for VoIP services (CallKit/PushKit). An SSH/mosh/ET client is not a VoIP service.
- `fetch`/`processing` are for `BGTaskScheduler` work, not a live session.

`BGTaskScheduler` constraints:

- The app registers a launch handler before the end of app launch and lists every permitted identifier in `BGTaskSchedulerPermittedIdentifiers` [S18][S19].
- `BGAppRefreshTask` is a short task for refreshing small amounts of content; it requires the `fetch` mode [S16].
- `BGProcessingTask` is for long data updates and can run for minutes, but the system can interrupt it; it runs only when the device is idle, and the system terminates it when the user starts using the device [S17].
- The system decides when tasks run; provide an expiration handler and report completion [S18].

Therefore BGTaskScheduler can support periodic sync, cache refresh, or background upload. It cannot keep an SSH/mosh/ET session alive. The app should treat background suspension as a disconnect and rely on mosh/ET reconnection or server-side `tmux`/`screen`; Termius's FAQ documents exactly this constraint and recommends those workarounds [S41].

MyGo's `plugins/background` adds `UIBackgroundModes` `fetch` and `processing` plus `BGTaskSchedulerPermittedIdentifiers` when imported, and uses `BGAppRefreshTask`/`BGProcessingTask` [S37]. It is review-legitimate for periodic sync, but importing it solely hoping to keep a session alive will not work and could look like background-mode misuse.

### 1.6 Privacy manifest, labels, and review mechanics

**Privacy manifest.** Since May 1, 2024, App Store Connect does not accept apps that use required-reason APIs without describing the reason in `PrivacyInfo.xcprivacy` [S20]. MyGo generates a manifest from the linked binary unless the project supplies `ios/PrivacyInfo.xcprivacy` [S37]. MyGo's generator includes reasons for file timestamps (`C617.1`), system boot time (`35F9.1`), disk space (`E174.1`), and user defaults (`CA92.1`), and declares no tracking and no collected data types [S37]. If Natcatty collects analytics, crash data linked to identity, or sync metadata, supply its own manifest and App Store privacy labels; do not rely on the generated "no data collected" declaration.

**App Review information and demo access.** Guideline 2.1 requires full access for review, including an active demo account or fully featured demo mode, plus any hardware or resources needed. If a demo account is impossible for legal/security reasons, a built-in demo mode requires prior Apple approval. [S1] For this app, provide a reachable SSH server plus mosh and ET endpoints, with `mosh-server` and `et` installed, and step-by-step review notes.

**IPv6-only.** Guideline 2.5.5 requires full functionality on IPv6-only networks [S1]. Verify SSH, mosh (UDP), and ET (TCP) over IPv6, including hostname resolution and fallback.

**Logging and credentials.** If the app records session logs, 2.5.14 requires explicit consent and a clear indication [S1]. Never log passwords or private keys; 5.1.1(vi) treats surreptitious password discovery as grounds for removal [S1].

## 2. Android and Google Play

### 2.1 Permissions MyGo already declares

MyGo's generated Android manifest declares `android.permission.INTERNET`, `android.permission.ACCESS_NETWORK_STATE`, and `android.permission.POST_NOTIFICATIONS`, plus any `android.permissions` from mygo.json and plugin permissions [S37].

- `INTERNET` protection level: normal; allows opening network sockets [S25].
- `ACCESS_NETWORK_STATE` protection level: normal; allows network information, used by MyGo's `Online`/`Metered` API [S25][S37].
- `POST_NOTIFICATIONS`: declared; MyGo requests it when it first posts a notification, not at launch [S37]. On Android 13+, a foreground-service notification needs this permission to appear in the notification drawer; without it, users may only see the service in Task Manager. Request it before starting the service.

### 2.2 `ACCESS_LOCAL_NETWORK`, target SDK 36 versus 37

Android's local network protection is the Android counterpart of Apple's local network permission:

- A local network is an IP network using a broadcast-capable interface such as Wi-Fi or Ethernet, excluding cellular (WWAN) and VPN [S24][S25].
- Operations requiring permission include outgoing TCP, accepting incoming TCP, and sending/receiving UDP unicast, multicast, or broadcast; resolving `.local` names also requires it. The checks apply to raw sockets, `NsdManager`, OkHttp, Cronet, and other APIs [S24].
- Exceptions include traffic to a DNS server on the local network at port 53 and system-mediated Output Switcher flows [S24].

Target SDK behavior:

| Target SDK | Permission | Default access | Required action |
|---|---|---|---|
| 36 (Android 16) | None for normal apps; `NEARBY_WIFI_DEVICES` only for the opt-in test | Local network open by default | None; apps with `INTERNET` receive an implicit grant. Do not declare or request `ACCESS_LOCAL_NETWORK`. |
| 37+ (Android 17) | `ACCESS_LOCAL_NETWORK` | Blocked by default for apps that update target SDK | Declare in manifest, check, and call `Activity.requestPermission()`; handle denial/revocation. |

Sources: [S24]. `ACCESS_LOCAL_NETWORK` was added in API level 37 and has protection level **dangerous**; it is required to advertise and connect to local network devices [S25]. It belongs to the `NEARBY_DEVICES` permission group; if the user has already granted another permission in that group, the system does not re-prompt [S24].

Denial behavior at target SDK 37:

- The app must gracefully handle denial or later revocation; local network traffic is blocked [S24].
- UDP failures produce `EPERM`; TCP typically times out, and native code can use `android_getnetworkblockedreason(int sockFd)` to detect `ANDROID_NETWORK_BLOCKED_REASON_LNP` [S24].
- System pickers (Cast output switcher, `NsdManager` service picker) avoid the broad permission for media casting and mDNS device selection, but they are not a general solution for an SSH client connecting to an arbitrary host. The direct-connection path needs the runtime permission [S24].

MyGo hardcodes `targetSdk = 36` and has no config override [S37]. That is compliant with Play's current requirement: from August 31, 2026, new apps and updates must target Android 16 (API 36) or higher, with an extension to November 1, 2026 [S23]. When Google requires API 37, MyGo must bump `compileSdk`/`targetSdk`; the app must then declare `ACCESS_LOCAL_NETWORK` and request it at runtime. MyGo's `permissionflow` plugin does not support `ACCESS_LOCAL_NETWORK` (its Android mapping covers camera, microphone, photos, contacts, location, Bluetooth, calendars, notifications) [S37], so this needs a custom MyGo plugin using `PluginHost.requestPermissions` or an upstream addition.

### 2.3 Foreground services and Play policy

Android 14+ requires every foreground service to declare a type and the corresponding type-specific permission; Play requires a declaration on the App content page [S26][S32]. Play's policy requires the use to be beneficial and core to the app, initiated by the user or user-perceptible, stoppable by the user, not deferrable without harming the expected feature, and run only as long as necessary; Play also requires a description, user impact, demo video, and use case selection [S32].

There is no SSH/terminal foreground-service type. The plausible choices:

| Type | Fit for a persistent SSH/mosh/ET session | Constraints |
|---|---|---|
| `dataSync` | Closest for ongoing network data transfer | `FOREGROUND_SERVICE_DATA_SYNC`; Android 15+ caps all `dataSync` services at 6 hours in any 24-hour period for target SDK 35+, then `Service.onTimeout`; user bringing the app foreground resets the timer [S27]. Play reviews the declaration; the policy exemption for `dataSync` only covers Play Asset Delivery [S32]. |
| `specialUse` | Possible for a terminal session not covered by other types | `FOREGROUND_SERVICE_SPECIAL_USE`; requires a `<property android:name="android.app.PROPERTY_SPECIAL_USE_FGS_SUBTYPE" android:value="...">` explaining the use case; Play reviews it [S26]. |
| `connectedDevice` | Weak fit: it is described as interactions with external devices requiring Bluetooth, NFC, IR, USB, or network; runtime prerequisites include `CHANGE_NETWORK_STATE`, `CHANGE_WIFI_STATE`, `NFC`, `TRANSMIT_IR`, Bluetooth runtime permissions, or USB permission [S26]. | Declaring a permission to satisfy the prerequisite does not make an SSH server an "external device"; misdeclaring the type is a Play policy risk. |

Additional constraints:

- Apps targeting Android 12+ cannot start a foreground service from the background except in specific exemption cases; start it while the user is in the app, when the session starts [S31].
- Android 14+ checks the type-specific permission when the service is created [S31].
- The foreground-service notification must be visible and accurate; request `POST_NOTIFICATIONS` first.
- Play requires the FGS declaration before review; missing or inaccurate declarations can block the release [S32].

### 2.4 Doze and App Standby

Doze reduces battery use by deferring background CPU and network activity when the device is unplugged, stationary, and screen-off. It suspends network access and defers jobs, syncs, and standard alarms until maintenance windows [S28]. App Standby defers background network activity for apps with no recent user activity [S28].

The power-management tables distinguish device state from app state: during Doze, network access is restricted; an app process running a foreground service has "No restrictions" in the app-state table, but the device-state Doze row still says network is restricted during Doze [S29]. Treat a foreground service as improving App Standby behavior, not as a guaranteed Doze exemption. A persistent TCP/UDP session can stall or break during Doze; design for reconnect.

The system offers a battery-optimization allowlist, but Google Play prohibits apps from requesting direct exemption from Doze/App Standby unless the core function is adversely affected [S28]. An SSH client can argue that losing the session breaks its core function, but this is a product/legal risk and should be tested with Play review; do not make the exemption the primary mechanism.

MyGo's `plugins/background` uses WorkManager on Android, not a foreground service; WorkManager periodic work has a 15-minute minimum and the worker has a roughly 10-minute limit [S37]. It is not a session keepalive.

### 2.5 Google Play review mechanics

- **Target API level:** API 36 required for new apps/updates from August 31, 2026; existing apps must target API 35 to remain available to new users on newer devices [S23]. MyGo's target SDK 36 is compliant now. A future API 37 requirement is not stated on the page as of 2026-11-04.
- **Data safety:** all developers must complete the Data safety form, including apps that collect nothing; declare collection/sharing and security practices such as encryption in transit. Data that is end-to-end encrypted and unreadable by the developer is not in scope for "collection" [S34]. If Natcatty's sync features send host data to a developer-controlled or third-party cloud, declare it.
- **App access:** if any part of the app is restricted by login credentials, memberships, location, or other authentication, provide sign-in instructions and all required details in Play Console [S35]. For an SSH client, provide a demo host, username, password/key, and any mosh/ET-specific setup.
- **Dynamic code:** Play's Device and Network Abuse policy prohibits an app from modifying, replacing, or updating itself outside Play, and prohibits downloading executable code such as dex, JAR, or `.so` files from a source other than Play. The exception is code running in a VM/interpreter with indirect Android API access [S33]. Bundled Go/`libmygoapp.so` and bundled plugin libraries are fine; runtime downloads of mosh/ET binaries, plugins, or scripts are not. Executing commands on the user's remote SSH server is not downloading code into the app.
- **16 KB page sizes:** apps targeting Android 15 (API 35)+ must support 16 KB memory pages on 64-bit devices on Play; starting February 1, 2027, updates without support cannot be released [S30]. MyGo passes `-Wl,-z,max-page-size=16384` for both the Go shared library and plugin C shared libraries [S37]. Verify any prebuilt mosh/ET `.so` files are also 16 KB aligned.
- **Export:** see section 1.4 and [S36].
- **Permissions:** declare only permissions the core functionality needs; `ACCESS_LOCAL_NETWORK` is necessary only when targeting API 37+, and `FOREGROUND_SERVICE_*` must match the declared type [S24][S26].

## 3. What to add to mygo.json, Info.plist, and AndroidManifest

The values below are examples; the export key and declarations need counsel/product decisions.

### 3.1 `mygo.json`

```json
{
  "ios": {
    "infoPlist": {
      "NSLocalNetworkUsageDescription": "Natcatty connects to SSH, mosh, and Eternal Terminal hosts on your Wi-Fi or local network.",
      "ITSAppUsesNonExemptEncryption": false
    }
  },
  "android": {
    "permissions": [
      "android.permission.FOREGROUND_SERVICE",
      "android.permission.FOREGROUND_SERVICE_DATA_SYNC"
    ]
  }
}
```

Notes:

- `NSLocalNetworkUsageDescription` must be added explicitly for release builds. MyGo adds it only for device debug builds (`opts.debug && !simulator`) [S37].
- `ITSAppUsesNonExemptEncryption` is MyGo's default `false`; set it only after counsel's export determination. If non-exempt, set `true` and add `ITSEncryptionExportComplianceCode` after Apple approves the documentation [S12].
- If the app browses/advertises Bonjour, add `NSBonjourServices` with the exact service types. If the app uses HTTP(S) to LAN addresses, review ATS; raw SSH/mosh/ET do not need ATS keys [S7].
- Do not add `ACCESS_LOCAL_NETWORK` while MyGo targets SDK 36; Android says not to declare or request it below target SDK 37 [S24]. Add it only when MyGo is bumped to 37.
- Background modes: `ios.backgroundModes` is available, but only list modes the app actually uses [S13]. Importing `plugins/background` already adds `fetch` and `processing`; do not add `audio` or `voip` for keepalive.
- `android.permissions` only declares permissions. A foreground-service component needs a plugin's `android.application` XML fragment (MyGo has no app-level `android.application` field) [S37].
- If a plugin needs an iOS entitlement (for example multicast), it must declare it under `mobile.ios.entitlements`, and the provisioning profile must already contain it [S37]. Not needed for unicast SSH/mosh/ET.

### 3.2 Info.plist keys

| Key | When | Source |
|---|---|---|
| `NSLocalNetworkUsageDescription` | Always, for release builds that connect to private/local addresses | [S2][S3] |
| `NSBonjourServices` | Only if browsing/advertising Bonjour | [S2][S5] |
| `ITSAppUsesNonExemptEncryption` | Every app; decide with counsel | [S12] |
| `ITSEncryptionExportComplianceCode` | If non-exempt and Apple approved documentation | [S12] |
| `UIBackgroundModes` | Only modes actually used; `fetch`/`processing` for BGTaskScheduler; never `audio`/`voip` for session keepalive | [S1][S13] |
| `BGTaskSchedulerPermittedIdentifiers` | If using BGTaskScheduler; MyGo's background plugin adds its identifiers | [S18][S19][S37] |
| `NSAppTransportSecurity` / `NSAllowsLocalNetworking` | Only for HTTP(S) to local/unqualified/IP addresses, not raw sockets | [S7] |

### 3.3 AndroidManifest additions

MyGo generates the base manifest with `INTERNET`, `ACCESS_NETWORK_STATE`, `POST_NOTIFICATIONS`, the launcher activity, and URL-scheme/app-link filters [S37]. App-side additions:

- Foreground service permissions:
  - `android.permission.FOREGROUND_SERVICE`
  - `android.permission.FOREGROUND_SERVICE_DATA_SYNC` or `android.permission.FOREGROUND_SERVICE_SPECIAL_USE`
- A `<service>` with `android:foregroundServiceType="dataSync"` (or `specialUse` with the `<property>` sub-element) and `android:exported="false"`, inserted through a MyGo plugin's `mobile.android.application` XML fragments [S26][S37].
- `ACCESS_LOCAL_NETWORK` only when targeting SDK 37+ [S24][S25].
- A notification channel for the foreground-service notification; request `POST_NOTIFICATIONS` at the appropriate time.

There is no MyGo config field for arbitrary `<application>` children; the plugin schema's `application` array is the supported route [S37].

### 3.4 Store-console declarations

App Store Connect:

- Export compliance questionnaire or `ITSAppUsesNonExemptEncryption`; upload French declaration if distributing in France; upload CCATS only if non-standard crypto [S9][S10][S12].
- App privacy labels and privacy policy URL [S1].
- App Review notes: demo SSH/mosh/ET hosts, credentials, mosh-server/et setup, background behavior (sessions reconnect; no background persistence), and local network purpose.

Google Play Console:

- Data safety form [S34].
- App access instructions with demo credentials [S35].
- Foreground-service declaration with description, user impact, video, and use case [S32].
- Target API 36 compliance [S23].
- 16 KB page-size check [S30].
- Export compliance per US EAR; verify whether Play Console asks a declaration [S36].

## 4. MyGo PR 940 versus app-side work

| Need | MyGo PR 940 | App-side |
|---|---|---|
| iOS Info.plist generation | Generates bundle keys, URL schemes, orientations, launch screen, background modes, plugin Info.plist; app keys win [S37] | Add `NSLocalNetworkUsageDescription`, export key decision, `NSBonjourServices`/ATS if used |
| iOS local network key | Only device debug builds get `NSLocalNetworkUsageDescription` and `NSAllowsLocalNetworking` [S37] | Release build must add the key; handle prompt/denial |
| iOS privacy manifest | Auto-generates from linked binary unless `ios/PrivacyInfo.xcprivacy` exists [S37] | Audit; override for analytics/tracking/collected data; privacy labels |
| iOS private APIs | Build refuses private Objective-C selectors [S37] | Plugins must also use public APIs |
| iOS background | `ios.backgroundModes` config; `plugins/background` adds `fetch`/`processing` and BGTaskScheduler identifiers [S37] | Use only for periodic sync; no audio/voip keepalive; reconnect sessions |
| iOS entitlements | Only plugins can declare; provisioning profile must already contain them [S37] | Multicast only if discovery (not needed for unicast); request Apple capability |
| Android manifest | Generates `INTERNET`, `ACCESS_NETWORK_STATE`, `POST_NOTIFICATIONS`; app permissions config; plugin `application` XML [S37] | Add FGS permissions/components; `ACCESS_LOCAL_NETWORK` only at target 37 |
| Android target SDK | Hardcoded `compileSdk = 36`, `targetSdk = 36`, `minSdk = 26` [S37] | Cannot override; needs MyGo change for 37 |
| Android local network runtime request | None; `permissionflow` lacks `ACCESS_LOCAL_NETWORK` [S37] | Custom plugin with `PluginHost.requestPermissions` when targeting 37 |
| Android 16 KB pages | Sets `max-page-size=16384` for Go and plugin C [S37] | Verify prebuilt mosh/ET `.so` files |
| Network API | `mygo.Network` exposes only `Online` and `Metered`, plus change notifications [S37] | Use Go `net` directly; no MyGo local-network or interface API |
| Lifecycle/session | Emits suspend/resume, `OnSaveState`/`RestoredState`, `StartedHidden`/`WasEndedBySystem` [S37] | Reconnect strategy, mosh/ET/tmux handling, user messaging |

## 5. mosh/ET review risks

### 5.1 Non-standard protocols and UDP

Mosh uses SSH to authenticate and start `mosh-server`, then a UDP State Synchronization Protocol on ports 60000-61000, with AES-128-OCB3 datagrams [S44]. ET uses SSH for authentication/encryption and a reconnectable TCP connection, default port 2022 [S45]. Neither is a standard IETF terminal protocol, but neither App Review guidelines nor Play policy prohibits custom protocols or UDP [S1][S33]. Review will not inspect the packets; it will test whether the app works and whether it follows privacy/permission rules.

The concrete constraints:

- On iOS, sending UDP unicast to a local address requires local network access; multicast/broadcast would additionally require the multicast entitlement [S2].
- On Android target SDK 37+, UDP to a local address requires `ACCESS_LOCAL_NETWORK`; denial produces `EPERM` [S24].
- Review networks may block UDP or port 2022. Provide SSH fallback and document it.
- The demo server must run `mosh-server` and `et`; otherwise reviewers see failures and may reject under 2.1 [S1].

### 5.2 Persistent sessions

mosh and ET are designed for roaming and reconnection: the remote server keeps the session, and the client reattaches after network changes [S44][S45]. That is compatible with iOS suspension and Android Doze because the app reconnects when it resumes. What is not compatible is claiming a live socket in the background:

- iOS suspends the app; Termius documents 20-30 seconds and recommends `tmux`/`screen` [S41].
- iOS background modes cannot legitimately keep the socket alive; audio/voip misuse violates 2.5.4 [S1][S13].
- Android foreground services can extend execution, but Doze can still restrict network and `dataSync` has a 6-hour daily cap [S27][S29].

Product copy should say "reconnects" or "session survives on the server", not "stays connected in the background".

### 5.3 In-process native libraries

- **iOS:** MyGo builds Go as a `c-archive` linked into the app's Swift executable [S37]. Bundled static code is allowed under 2.5.2; runtime downloads are not [S1]. MyGo's private-API scan reduces 2.5.1 risk [S37]. The generated privacy manifest covers the Go runtime's required-reason APIs; if plugin native frameworks use required-reason APIs, verify whether they need their own `PrivacyInfo.xcprivacy` [S20][S37].
- **Android:** MyGo builds Go as `libmygoapp.so` per ABI and compiles plugin C into bundled `.so` files, with 16 KB page alignment [S37]. Play allows bundled native libraries but forbids downloading executable code at runtime [S33]. Verify every prebuilt mosh/ET library is 16 KB aligned and stripped as appropriate [S30].
- **Export:** native crypto in Go/mosh/ET is still encryption software; the export analysis in section 1.4 applies to all of it [S8][S10][S21][S22].

### 5.4 Review test plan

1. Provide a public demo SSH host, a mosh endpoint with `mosh-server`, and an ET endpoint with `et`; include credentials and exact steps.
2. Test on IPv6-only and on a LAN with a private address; confirm the local network prompt and denial handling on a real iOS device [S1][S2].
3. Test background/resume: the app should show reconnection, not a dead session.
4. Provide review notes explaining mosh UDP and ET TCP, and why the app reconnects rather than running in the background.
5. Confirm no runtime downloads, no private APIs, and no silent audio/VoIP.
6. Confirm the App Store privacy labels and Play Data safety form match actual behavior [S1][S34].

## 6. Unknowns / needs verification

1. **Export classification is a legal determination.** Whether the app can set `ITSAppUsesNonExemptEncryption` to `NO`, whether any component needs a CCATS, and the exact mass-market self-classification/filing are for counsel and BIS. Apple's reference suggests industry-standard app crypto needs only a French declaration, but the US annual report is separate [S10][S21][S22].
2. **Missing `NSLocalNetworkUsageDescription` behavior for direct unicast sockets is not fully documented.** Apple says apps should include it [S3]; WWDC20 shows Bonjour apps without it and without service types are blocked with `NoAuth` and no prompt [S6]; TN3179 says to add it and handle errors [S2]. Do not ship without it; test on a real device if the key is omitted.
3. **Which Android foreground-service type Play will approve for a persistent terminal session.** `dataSync` has a 6-hour daily cap; `specialUse` requires Play review; `connectedDevice` is framed for external devices. Pre-review with Play or use `specialUse` with a clear rationale [S26][S27][S32].
4. **When Google Play will require API 37.** The current page states API 36 from August 31, 2026; the next annual requirement is not stated as of 2026-11-04 [S23]. MyGo's hardcoded 36 will eventually need an upstream bump.
5. **Whether Play Console presents an encryption/export declaration.** Google's export page states the legal obligation and points to BIS but does not describe a per-build questionnaire [S36].
6. **Whether MyGo's plugin native frameworks need their own privacy manifests.** Apple requires a manifest for each executable/dynamic library that uses required-reason APIs [S20]; MyGo's generated manifest is at the app bundle level [S37]. Audit plugin frameworks.
7. **Whether prebuilt mosh/ET `.so` files are 16 KB aligned.** MyGo's own builds are; third-party prebuilts need verification [S30][S37].
8. **OEM battery managers and Doze behavior in practice.** AOSP docs describe restrictions, but device-specific killers vary; test a device matrix. Do not rely on a battery-optimization exemption without Play policy review [S28].
9. **Whether Natcatty's sync/telemetry counts as data collection.** If data goes to a developer-controlled or third-party service, App Store privacy labels and Play Data safety must declare it; SSH traffic itself is end-to-end between the app and the user's server [S1][S34].
10. **Whether iOS treats CGNAT/VPN addresses as local network addresses.** TN3179 defines local network by broadcast-capable interface and excludes VPN; test the specific deployment networks [S2].
11. **Whether App Review accepts the app's background/reconnect claims and demo setup.** Provide demo servers and review notes; 2.1 is discretionary [S1].
12. **Whether `ACCESS_LOCAL_NETWORK` can be requested through a custom MyGo plugin at target SDK 37.** `PluginHost.requestPermissions` accepts arbitrary permission names [S37], but the MyGo target SDK must first be bumped and the runtime flow tested.

## Sources

All URLs accessed 2026-11-04. Apple documentation is living; dates are what the page or revision history reported on that date.

| ID | Source | Version / date |
|---|---|---|
| S1 | Apple, App Store Review Guidelines, https://developer.apple.com/app-store/review/guidelines/ | Last updated 2026-10-16 |
| S2 | Apple, TN3179: Understanding local network privacy, https://developer.apple.com/documentation/technotes/tn3179-understanding-local-network-privacy | Revision history updated 2026-10-06 |
| S3 | Apple, `NSLocalNetworkUsageDescription`, https://developer.apple.com/documentation/bundleresources/information-property-list/nslocalnetworkusagedescription | Introduced iOS 14; accessed 2026-11-04 |
| S4 | Apple, `com.apple.developer.networking.multicast`, https://developer.apple.com/documentation/bundleresources/entitlements/com.apple.developer.networking.multicast | Introduced iOS 14; accessed 2026-11-04 |
| S5 | Apple, `NSBonjourServices`, https://developer.apple.com/documentation/bundleresources/information-property-list/nsbonjourservices | Accessed 2026-11-04 |
| S6 | Apple, WWDC20 session 10110, "Support local network privacy in your app", https://developer.apple.com/videos/play/wwdc2020/10110/ | 2020; transcript accessed 2026-11-04 |
| S7 | Apple, `NSAllowsLocalNetworking`, https://developer.apple.com/documentation/bundleresources/information-property-list/nsapptransportsecurity/nsallowslocalnetworking | Accessed 2026-11-04 |
| S8 | Apple, Complying with Encryption Export Regulations, https://developer.apple.com/documentation/security/complying-with-encryption-export-regulations | Accessed 2026-11-04 |
| S9 | Apple, App Store Connect Help, Overview of export compliance, https://developer.apple.com/help/app-store-connect/manage-app-information/overview-of-export-compliance/ | Accessed 2026-11-04 |
| S10 | Apple, App Store Connect Reference, Export compliance documentation for encryption, https://developer.apple.com/help/app-store-connect/reference/app-information/export-compliance-documentation-for-encryption | Accessed 2026-11-04 |
| S11 | Apple, App Store Connect Help, Determine and upload app encryption documentation, https://developer.apple.com/help/app-store-connect/manage-app-information/determine-and-upload-app-encryption-documentation/ | Accessed 2026-11-04 |
| S12 | Apple, `ITSAppUsesNonExemptEncryption`, https://developer.apple.com/documentation/bundleresources/information-property-list/itsappusesnonexemptencryption | Accessed 2026-11-04 |
| S13 | Apple, Configuring background execution modes, https://developer.apple.com/documentation/xcode/configuring-background-execution-modes | Copyright 2026; accessed 2026-11-04 |
| S14 | Apple, `UIBackgroundModes`, https://developer.apple.com/documentation/bundleresources/information-property-list/uibackgroundmodes | Accessed 2026-11-04 |
| S15 | Apple, `BGTaskScheduler`, https://developer.apple.com/documentation/backgroundtasks/bgtaskscheduler | Accessed 2026-11-04 |
| S16 | Apple, `BGAppRefreshTask`, https://developer.apple.com/documentation/backgroundtasks/bgapprefreshtask | Accessed 2026-11-04 |
| S17 | Apple, `BGProcessingTask`, https://developer.apple.com/documentation/backgroundtasks/bgprocessingtask | Accessed 2026-11-04 |
| S18 | Apple, Using background tasks to update your app, https://developer.apple.com/documentation/uikit/using-background-tasks-to-update-your-app | Accessed 2026-11-04 |
| S19 | Apple, `BGTaskSchedulerPermittedIdentifiers`, https://developer.apple.com/documentation/bundleresources/information-property-list/bgtaskschedulerpermittedidentifiers | Accessed 2026-11-04 |
| S20 | Apple, Describing use of required reason API, https://developer.apple.com/documentation/bundleresources/describing-use-of-required-reason-api | Enforcement from 2024-05-01; accessed 2026-11-04 |
| S21 | 15 CFR § 742.15, Encryption items, https://www.ecfr.gov/current/title-15/subtitle-B/chapter-VII/subchapter-C/part-742/section-742.15 | eCFR current as of 2026-10-30 |
| S22 | 15 CFR § 740.17, License Exception ENC, https://www.ecfr.gov/current/title-15/subtitle-B/chapter-VII/subchapter-C/part-740/section-740.17 | eCFR current as of 2026-10-30 |
| S23 | Google Play, Target API level requirement, https://developer.android.com/google/play/requirements/target-sdk | Last updated 2026-10-28 |
| S24 | Android Developers, Local network permission, https://developer.android.com/privacy-and-security/local-network-permission | Last updated 2026-10-02 |
| S25 | Android Developers, `Manifest.permission`, https://developer.android.com/reference/android/Manifest.permission | Accessed 2026-11-04; `ACCESS_LOCAL_NETWORK` added API 37 |
| S26 | Android Developers, Foreground service types, https://developer.android.com/develop/background-work/services/fgs/service-types | Last updated 2026-10-28 |
| S27 | Android Developers, Android 15 behavior changes, https://developer.android.com/about/versions/15/behavior-changes-15 | Last updated 2026-10-28 |
| S28 | Android Developers, Doze and App Standby, https://developer.android.com/training/monitoring-device-state/doze-standby | Last updated 2026-10-22 |
| S29 | Android Developers, Power management restrictions, https://developer.android.com/topic/performance/power/power-details | Last updated 2026-05-19 |
| S30 | Android Developers, Support 16 KB page sizes, https://developer.android.com/guide/practices/page-sizes | Last updated 2026-10-16 |
| S31 | Android Developers, Restrictions on starting a foreground service from the background, https://developer.android.com/develop/background-work/services/fgs/restrictions-bg-start | Last updated 2026-10-28 |
| S32 | Google Play, Understanding foreground service and full-screen intent requirements, https://support.google.com/googleplay/android-developer/answer/13392821 | Accessed 2026-11-04 |
| S33 | Google Play, Device and Network Abuse, https://support.google.com/googleplay/android-developer/answer/16559646 | Accessed 2026-11-04 |
| S34 | Google Play, Provide information for Google Play's Data safety section, https://support.google.com/googleplay/android-developer/answer/10787469 | Accessed 2026-11-04 |
| S35 | Google Play, Prepare your app for review / app access, https://support.google.com/googleplay/android-developer/answer/9859455 | Accessed 2026-11-04 |
| S36 | Google Play, Export compliance, https://support.google.com/googleplay/android-developer/answer/113770 | Accessed 2026-11-04 |
| S37 | MyGo PR 940 (`egoist/mygo#940`), branch `mobile`, head `7153b6c6`, https://github.com/egoist/mygo/pull/940. Files inspected: `docs/mobile.md`, `cmd/mygo/config.go`, `cmd/mygo/ios.go`, `cmd/mygo/android.go`, `cmd/mygo/mobile_plugins.go`, `internal/mobile/services.go`, `internal/mobile/android/app/build.gradle.kts`, `plugins/background/*`, `plugins/permissionflow/android/PermissionFlowPlugin.kt` | Accessed 2026-11-04; raw files confirmed at 7153b6c6 |
| S38 | Apple iTunes Lookup API, Prompt 3 (Panic, Inc.), https://itunes.apple.com/lookup?id=1594420480&country=us | Version 3.5.3, accessed 2026-11-04 |
| S39 | Apple iTunes Lookup API, Blink Shell, Build & Code, https://itunes.apple.com/lookup?id=1594898306&country=us | Version 18.7.1, accessed 2026-11-04 |
| S40 | Apple iTunes Lookup API, Termius - Modern SSH Client, https://itunes.apple.com/lookup?id=549039908&country=us | Version 8.0.7, accessed 2026-11-04 |
| S41 | Termius documentation, How can I keep Termius sessions alive in the background on iOS/iPadOS?, https://docs.termius.com/help-center/faq/how-can-i-keep-termius-sessions-alive-in-the-background-on-ios-ipados | Accessed 2026-11-04 |
| S42 | Apple iTunes Lookup API, iSH Shell, https://itunes.apple.com/lookup?id=1436902243&country=us | Version 1.3.2, accessed 2026-11-04 |
| S43 | Google Play, ConnectBot: SSH & Mosh client, https://play.google.com/store/apps/details?id=org.connectbot | Accessed 2026-11-04 |
| S44 | Mosh project, https://mosh.org/ | Accessed 2026-11-04 |
| S45 | Eternal Terminal README, https://github.com/MisterTea/EternalTerminal | Master, accessed 2026-11-04 |
