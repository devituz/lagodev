# Social preview image spec

GitHub renders a repository's "social preview" image when someone
shares the repo URL on Twitter, LinkedIn, Slack, Discord, etc. A good
image lifts click-through dramatically; a missing one falls back to a
generic GitHub octocat that looks like every other repo on the
platform.

> **Where to upload it:**
> https://github.com/devituz/lagodev/settings → scroll to
> **Social preview** → drag-drop the file. Only repo admins can
> change it.

## Format

| Attribute | Value |
|---|---|
| Dimensions | **1280 × 640** px (2:1 aspect) |
| Format | PNG or JPG |
| Max size | 1 MB |
| Color space | sRGB |
| Transparency | Not preserved — assume solid background |

GitHub will downscale to ~1200 × 600 for display. Avoid putting
critical text near the bottom 60 px — Twitter/X often crops there.

## Content checklist

The image should convey, in under a second:

1. **The name** — `lagodev` (large, top-left or center)
2. **The tagline** — "Laravel-grade developer experience for Go" or a
   short variant
3. **A visual identifier** — language mark (Go gopher / Go logo) +
   inspiration mark (Laravel red) so the "Laravel for Go" hook is
   immediate
4. **No URL** — the URL is already shown in the link card

## Suggested layouts

### Layout A — split

```
┌──────────────────────────┬─────────────────────────┐
│                          │                         │
│   lagodev                │   ┌────┐                │
│                          │   │ Go │  + Laravel    │
│   Laravel-grade DX       │   └────┘                │
│   for Go                 │                         │
│                          │                         │
└──────────────────────────┴─────────────────────────┘
   Left 60% text           Right 40% visual
```

### Layout B — code snippet

```
┌──────────────────────────────────────────────────────┐
│  lagodev                                             │
│  Laravel-grade DX for Go                             │
│                                                      │
│  ┌─────────────────────────────────────────────────┐│
│  │ app := web.New()                                 ││
│  │ app.Resource("posts", &PostController{})         ││
│  │ app.MustRun(":8080")                             ││
│  └─────────────────────────────────────────────────┘│
└──────────────────────────────────────────────────────┘
```

Layout B's "look at this short snippet" technique tends to convert
slightly better for developer audiences — people stop scrolling
because they can read the API in the preview.

## Palette suggestion

| Role | Hex | Usage |
|---|---|---|
| Background | `#0F1419` | Dark; reads well in light + dark Twitter themes |
| Primary text | `#FFFFFF` | High contrast |
| Accent | `#FF2D20` | Laravel red — explicit nod |
| Code bg | `#1E2429` | Slightly lighter than background |
| Code text | `#7FE9D8` | Soft cyan; Go-ish |
| Secondary text | `#9DA5B4` | Tagline / metadata |

Adjust to whatever you already use elsewhere — consistency across
README badges, docs, and the social preview matters more than the
exact palette.

## Tools

- [Figma](https://figma.com) — there's a community template
  "GitHub social preview 1280×640" you can clone.
- [Canva](https://canva.com) — has a "GitHub preview" preset.
- Plain ImageMagick / SVG export — works if you have a brand asset
  already.

## Quick check

Once uploaded, validate the preview in three places:

1. https://www.opengraph.xyz/?url=https://github.com/devituz/lagodev
2. Post the URL in a private Slack DM to yourself — Slack renders
   the OG image inline.
3. Tweet the URL from a draft / scheduled tweet and inspect the
   card.

GitHub caches the OG image aggressively — re-uploads can take 10–30
minutes to reflect on third-party social platforms.
