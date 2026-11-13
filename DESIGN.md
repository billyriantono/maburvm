# Design direction

The panel is an instrument for running machines. It should read like one: calm
metal, exact numbers, and the state of a machine visible before anything else.

Chosen by the project owner on 2026-11-13 from three explored directions
("Recorder"). Tokens live in `web/src/app/globals.css`; this file is the
reasoning behind them and the rules for new screens.

Dial: ENERGY 2 / RHYTHM 2 / MOTION 2.

## Who it is for

Operators who watch a fleet, and customers who rent one or a few servers. Both
come to a screen with the same two questions: is it healthy, and what can I do
about it. Every screen is built around the decision made there, not around a
default layout.

## Colour

| Role | Light | Dark | Why |
|---|---|---|---|
| Ground | `#E9ECEF` | `#0F1418` | Cool slate, the colour of the hardware. Recedes behind the panels. |
| Panel | `#FAFBFC` | `#1A2128` | Where reading happens. |
| Ink | `#16202A` | `#E6EBF0` | Blue-black, never pure black: it sits in the slate family. |
| Quiet ink | `#4A5967` | `#A3B0BD` | Labels and secondary text. At least 6:1 on any panel. |
| Rule | `#C3CBD3` | `#34414D` | Panel edges. A lighter "hair" rule divides rows inside a panel. |
| Faceplate | `#1B2530` | `#232E3A` | See below. Dark in both themes. |
| Brand | `#1F4FD8` | `#3D6AE6` | The operator's colour. Replaced by theirs when they set one. |

State colours (green running, amber busy or warning, red failed, grey off) are
separate from the brand colour and never used as decoration. A state colour
always sits beside a word.

**The brand colour marks two things only:** where you are (the current tab,
page or selected range) and the one obvious next action (Start on a stopped
machine, Save on a changed form). If it appears anywhere else, remove it.

Charts use two fixed hues, blue `#2A78D6` and orange `#EB6834` (dark:
`#3987E5`, `#D95926`). They were checked as a pair for colour-blind separation
on both panel colours. Blue is always the first series (in, read), orange the
second (out, write). Text never takes a chart colour.

## Type

One family: **Archivo**, with its width axis doing the work of a second face.

- Normal width for everything read.
- Wide (`font-stretch: 118%`) for the name of a machine or of the operator,
  like a label engraved on a front panel. Nothing else is set wide.
- **IBM Plex Mono** only for strings people copy: addresses, commands, keys,
  IDs. It is there because 0/O and 1/l must look different, not for style.
  Labels and numbers stay in Archivo.
- Digits are tabular everywhere, so polled values do not shift.
- Sentence case. No all-caps labels, no letter-spaced labels.

## Shape and depth

- Radius is small and consistent: 6px panels, 4px controls. No pills.
- Panels are flat with a 1px rule. Shadow is reserved for things that float
  above the page (menus, dialogs, popovers).
- No gradients, glows, glass or background patterns. The sign-in artwork is the
  one stated exception (see below).

## The faceplate

A machine's page opens with a dark strip carrying its state lamp, its name,
its power controls, and the identifiers people copy. It is dark in both themes
(class `faceplate`), which makes it the one thing on the page that looks like
the machine itself. Use it for a single machine or node, never as a generic
page header.

## The sign-in page

Two zones: a picture side and a form side with room around it. The picture
side is dark in both themes and carries the operator's name and a lamp that
times a real request to the panel.

The operator chooses the picture (Branding, Sign-in page):

- **Traces** (the default): the product's own stacked reading traces, drawn
  large in the operator's hue and worked out from their name, so every panel
  has its own picture without shipping stock imagery. This is the one place a
  gradient and glow are used: here they are the subject, light arriving at
  "now", not a finish applied to a surface. It is artwork and carries no
  figures, so it cannot be mistaken for a measurement.
- **Logo**: their logo, large, on a flat ground of their colour.
- **Picture**: a photograph or artwork of their own, shaded only under the
  two lines of text.

The brand studio previews the real page, scaled, never a redrawing of it.

## Readings

Time-based readings are drawn as thin strips stacked on one shared time axis,
each with its own scale and its current value in words beside it. One
crosshair reads every strip at once.

- No reading is drawn for time when nothing was recorded. A gap is information.
- Each strip states its top value. Never two scales on one strip.
- The value shown is the live one; while inspecting a moment it is that
  moment's. "Off", "No reading" and "Not reported" are different states and
  say so.
- A table of the same readings is always one click away.

## Words

Name things the way the person using them would. Buttons say what happens
(Restart, Stop, Assign address), and the confirmation and the result use the
same word. Anything that stops a running machine or destroys data asks first
and says what will happen in plain terms. Empty and failed states say why and
what to do next.

## Motion

Things move when the person acts (a dialog opens, a state changes) or when
data arrives. Nothing loops for decoration. All of it stops under
`prefers-reduced-motion`.

## Icons

Controls are labelled in words. An icon may accompany a label in navigation;
it never replaces one.
