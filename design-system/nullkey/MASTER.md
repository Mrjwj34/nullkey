# Nullkey Design System Master

> Global source of truth for the Nullkey macOS desktop prototype.
>
> Page overrides, when needed, live in `pages/<page-name>.md` and only override
> the named section. Product behavior remains governed by `docs/08-api-contract.md`
> and `docs/10-interaction-contract.md`.

## 1. Design Direction

**2026-10-24 user-approved prototype revision:** the current prototype adopts a
graphite dark cockpit, a single workspace tab row, and a contextual isolation
boundary inspector. This supersedes the system-following light default for this
prototype. Implementation theme mappings live in `design/cockpit.css`; all safety
and credential boundary requirements below remain applicable.

| Dial | Value | Meaning |
|---|---:|---|
| Variance | 3/10 | Stable, centered, predictable composition |
| Motion | 2/10 | Feedback-only motion; no decorative reveal |
| Density | 8/10 | Compact desktop workspace with strong grouping |

**Style:** native macOS utility + restrained Swiss minimalism.

**Product character:** quiet, exact, local, trustworthy. Nullkey is a daily
developer tool, not a security theater dashboard and not a hacker-themed demo.

**Primary experience:** workspace-centered terminal. Security status is always
legible but never competes with the work surface.

### Explicitly rejected generator matches

- FAQ/documentation landing-page structure: wrong product pattern.
- Monospace for all interface copy: hurts scanning and reads as costume.
- Neon green on navy as the product identity: overstates “cybersecurity”.
- Scroll-reveal animations: inappropriate for a persistent desktop workspace.
- Card grids as page structure: add chrome without improving hierarchy.

## 2. Semantic Color

Use semantic tokens in components. Raw values only belong in the theme mapping.

### Light

| Token | Value | Purpose |
|---|---|---|
| `canvas` | `#F5F6F7` | Window content background |
| `surface` | `#FFFFFF` | Sidebar, toolbar, modal, popover |
| `surface-raised` | `#FFFFFF` | Menu and dialog foreground |
| `surface-subtle` | `#ECEFF1` | Selected rows and grouped controls |
| `text-primary` | `#171A1D` | Primary text |
| `text-secondary` | `#515B65` | Secondary text; must remain >= 4.5:1 |
| `text-tertiary` | `#69747E` | Metadata on white only |
| `border` | `#D2D8DD` | Dividers and control outlines |
| `border-strong` | `#AAB4BC` | Hover and selected boundaries |
| `accent` | `#176B5B` | Primary action and active navigation |
| `accent-hover` | `#125548` | Primary hover |
| `accent-subtle` | `#E3F1ED` | Selected navigation background |
| `focus` | `#1677C8` | Keyboard focus ring |
| `success` | `#147A52` | Connected and running |
| `warning` | `#9A5700` | Open network and expiring state |
| `danger` | `#B4232D` | Failure and irreversible deletion |
| `info` | `#2864A8` | Preparing and informational state |
| `terminal-bg` | `#111416` | Terminal only |
| `terminal-fg` | `#E7EAEC` | Terminal only |

### Dark

| Token | Value | Purpose |
|---|---|---|
| `canvas` | `#15181A` | Window content background |
| `surface` | `#1D2124` | Sidebar, toolbar, modal, popover |
| `surface-raised` | `#252A2E` | Menu and dialog foreground |
| `surface-subtle` | `#292F33` | Selected rows and grouped controls |
| `text-primary` | `#F1F3F4` | Primary text |
| `text-secondary` | `#C1C8CD` | Secondary text |
| `text-tertiary` | `#A8B1B8` | Metadata |
| `border` | `#3C444A` | Dividers and control outlines |
| `border-strong` | `#5C6871` | Hover and selected boundaries |
| `accent` | `#54B7A0` | Primary action and active navigation |
| `accent-hover` | `#70C7B3` | Primary hover |
| `accent-subtle` | `#203E38` | Selected navigation background |
| `focus` | `#62AEF0` | Keyboard focus ring |
| `success` | `#5AC493` | Connected and running |
| `warning` | `#E6A34A` | Open network and expiring state |
| `danger` | `#F07A82` | Failure and irreversible deletion |
| `info` | `#70B1EA` | Preparing and informational state |
| `terminal-bg` | `#0D0F10` | Terminal only |
| `terminal-fg` | `#E7EAEC` | Terminal only |

### Color rules

- Normal text must reach 4.5:1; meaningful icons and focus boundaries 3:1.
- Never encode status with color alone. Pair color with text and a stable icon.
- Green means healthy/connected, not “secure”.
- Red is reserved for failure and irreversible data deletion.
- `open` network uses warning amber; it is not a fatal state.
- Test both themes independently; do not derive dark mode by inversion.

## 3. Typography

Use installed system fonts. Do not add a web-font dependency to a local desktop
tool.

| Role | Font | Size / line | Weight |
|---|---|---|---|
| Page title | SF Pro Display | 20 / 28 | 600 |
| Section title | SF Pro Text | 15 / 20 | 600 |
| Body | SF Pro Text | 13 / 20 | 400 |
| Label | SF Pro Text | 12 / 16 | 500 |
| Metadata | SF Pro Text | 12 / 16 | 400 |
| Code/path/ID | SF Mono | 12 / 18 | 400 |
| Terminal | SF Mono, user-selectable | 13 / 19 | 400 |

- Letter spacing remains `0`.
- Use tabular figures for CPU, memory, duration, port, and timestamps.
- Long paths and IDs use middle truncation only when space is constrained; the
  full value remains keyboard-accessible through copy or an anchored tooltip.
- UI text supports 200% text scaling without clipping primary actions or errors.

## 4. Spacing And Geometry

Density is compact, not cramped. Use a 4 px base grid.

| Token | Value | Use |
|---|---:|---|
| `space-1` | 4 px | Icon-to-label micro gap |
| `space-2` | 8 px | Compact control gap |
| `space-3` | 12 px | Row padding |
| `space-4` | 16 px | Panel padding |
| `space-6` | 24 px | Section gap |
| `space-8` | 32 px | Page-level separation |

| Geometry | Value |
|---|---:|
| Small control | 28 px high |
| Default control | 32 px high |
| Setup primary control | 36 px high |
| Icon hit target | 32 x 32 px minimum on desktop |
| Sidebar | 248 px expanded / 64 px collapsed |
| Session header | 52 px high |
| Status bar | 28 px high |
| Inspector | 320 px default, 280–420 px resizable |
| Button/input radius | 6 px |
| Menu/dialog radius | 8 px |
| Status badge radius | 999 px |

Use separators and spacing for page hierarchy. Cards are limited to repeated
selectable entities, modals, and genuinely framed tools. Never nest cards.

## 5. Application Shell

```text
┌ macOS titlebar ────────────────────────────────────────────────┐
│ sidebar 248 │ session header: context · state · network · run │
│             ├──────────────────────────────────────────────────┤
│ workspaces  │                                                  │
│ recent      │ terminal / artifacts / services / desktop       │
│             │                                                  │
│ management  │                                      inspector   │
│             ├──────────────────────────────────────────────────┤
│             │ status bar                                      │
└─────────────┴──────────────────────────────────────────────────┘
```

- Reference frame: 1440 x 900.
- Default window: 1280 x 820.
- Minimum window: 1024 x 700.
- Below 1120 px, collapse the sidebar before shrinking the work surface.
- Terminal minimum is 640 x 420.
- The app is desktop-only; do not turn narrow windows into a mobile card feed.

## 6. Component Rules

### Buttons

- One primary button per visible region.
- Primary: filled accent. Secondary: neutral surface with border. Ghost: tools.
- Danger is reserved for irreversible deletion or credential revocation in the
  confirmation dialog; stopping a sandbox is neutral.
- Async buttons preserve width, show a leading spinner, set busy state, and
  reject duplicate activation while allowing a separate cancel action.
- Hover/pressed states change color or opacity without transforms or layout shift.

### Focus

- Every interactive control receives a 2 px focus ring with 2 px offset.
- Focus order follows visual order.
- Banners, sticky headers, and the terminal overlay must never obscure focus.
- `Cmd+Shift+M` moves focus out of the terminal to workspace navigation.

### Status

- Status badges are static labels, never clickable pills.
- Vocabulary is fixed: 准备中、运行中、已停止、启动失败、正在取消、
  已取消、已连接、需要授权、访问被拒绝.
- Live status announcements are complete contextual phrases and do not move focus.

### Feedback

- Inline error: field- or action-specific.
- Banner: contextual issue requiring recovery.
- Toast: brief completion or acknowledgement; never steals focus.
- Modal: irreversible confirmation or blocking decision only.
- Lists with 50+ rows use virtualization while preserving keyboard position.
- Existing data remains visible during refresh; reserve space for async content.

### Icons

- Use one 1.5 px outline icon family, preferably Lucide.
- Use official provider logos without recoloring or altered proportions.
- Decorative icons beside visible text are hidden from accessibility APIs.
- Icon buttons have accessible names and expose selected/expanded state.
- Do not use emoji as product icons.

## 7. Motion

| Token | Duration | Use |
|---|---:|---|
| `motion-feedback` | 80 ms | Press/hover feedback |
| `motion-content` | 120 ms | Same-container crossfade |
| `motion-panel-enter` | 160 ms | Inspector or popover enter |
| `motion-panel-exit` | 100 ms | Inspector or popover exit |

- Animate opacity and transform only.
- Motion must express state or spatial continuity; no decorative entrance reveals.
- All transitions are interruptible and set semantic state directly; correctness
  never depends on `animationend` or `transitionend`.
- Reduced motion replaces translation with <=100 ms crossfades or no animation.
- Progress bars reflect real progress. Unknown progress is indeterminate.

## 8. Security Content Rules

- Never display or copy credential plaintext or per-sandbox tokens.
- Never describe `services_only` as offline; model, MCP, and private Git gateway
  traffic remains available.
- Never claim a project is secret-free. A scan can only state that registered
  credentials were not found in generated runtime configuration/environment.
- Runtime isolation is not described as VM-grade or protection from a malicious Agent.
- External workspace copy: “Nullkey 会直接修改此目录。目录内已有凭证不受
  Nullkey 凭证隔离保证覆盖。”
- A policy failure blocks launch. There is no “continue without sandbox” action.

## 9. Accessibility And Quality Gates

- WCAG 2.2 AA baseline for text, focus visibility, keyboard navigation, status,
  forms, and error recovery.
- Full keyboard path for onboarding, OAuth cancel, terminal escape, reconnect,
  filters, and destructive confirmation.
- VoiceOver reading order matches visual order; terminal output is not continuously
  announced.
- 200% text scaling preserves all primary actions, cancel controls, and errors.
- Light, dark, increased contrast, reduced transparency, and reduced motion are
  tested independently.
- 1024 x 700, 1280 x 820, and 1440 x 900 have no overlap or horizontal page scroll.
- Dynamic content cannot resize fixed toolbars, status controls, or terminal bounds.

## 10. Pre-Delivery Checklist

- [ ] Primary workflow remains workspace -> session -> terminal.
- [ ] Only one primary action appears per visible region.
- [ ] All async actions expose accepted, progress, success, cancel, conflict, and failure states.
- [ ] Focus is visible and never hidden by fixed UI.
- [ ] Icon buttons have labels; status is not color-only.
- [ ] Both themes meet contrast requirements.
- [ ] Reduced motion and 200% text scaling are usable.
- [ ] No layout-shifting hover effects or decorative scroll animation.
- [ ] No secret, token, Authorization, Cookie, body, or query string is shown.
- [ ] Stop, delete record, clear state, and delete files remain distinct actions.
- [ ] `services_only` and scan scope use contract-accurate copy.
- [ ] Window size matrix passes without overlap or clipped controls.
