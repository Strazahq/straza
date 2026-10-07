# Fonts of the docs site

The docs site ships its two typefaces, so a page loads nothing from another host and the
demo stack's docs work offline. Both files are variable fonts with the latin subset only.
The stylesheet `../site.css` declares them and falls back to the system fonts.

| File | Family | Version | Weights used |
|---|---|---|---|
| `atkinson-hyperlegible-next-latin.woff2` | Atkinson Hyperlegible Next | Version 2.001, Google Fonts v7 | 400, 500, 700, 800 |
| `atkinson-hyperlegible-mono-latin.woff2` | Atkinson Hyperlegible Mono | Version 2.001, Google Fonts v8 | 400, 600 |

The version is the name table's version string, read from each file.

## Source

Downloaded on 2026-10-06 from the latin blocks of this stylesheet, requested with a
Chrome 141 User-Agent so that it lists WOFF2 files:

`https://fonts.googleapis.com/css2?family=Atkinson+Hyperlegible+Mono:wght@400;600&family=Atkinson+Hyperlegible+Next:wght@400;500;700;800&display=swap`

- Next: `https://fonts.gstatic.com/s/atkinsonhyperlegiblenext/v7/NaPNcYPdHfdVxJw0IfIP0lvYFqijb-UxCtm5_wdGseiJn3o.woff2`,
  SHA-256 `18b2a1a39a2fa298b0ba5390aca68462669826c90925656f1c1f6796e0e1bbaf`
- Mono: `https://fonts.gstatic.com/s/atkinsonhyperlegiblemono/v8/tss4AoFBci4C4gvhPXrt3wjT1MqSzhA4t7IIcncBiwKthFw.woff2`,
  SHA-256 `2706b1ee4f452e744ea91f7e4908cbde9c5d35521bf5ffffc71a382a2de89613`

The upstream projects are https://github.com/googlefonts/atkinson-hyperlegible-next and
https://github.com/googlefonts/atkinson-hyperlegible-next-mono.

## License

Both fonts are under the SIL Open Font License 1.1. `OFL.txt` beside them holds the license
text with both copyright lines, taken from the `ofl/atkinsonhyperlegiblenext/OFL.txt` and
`ofl/atkinsonhyperlegiblemono/OFL.txt` files of https://github.com/google/fonts. The license
allows bundling the fonts with software, and neither copyright line declares a Reserved
Font Name. The files are stored as Google Fonts serves them, unchanged.

To refresh them, request the stylesheet above again, download the two latin files, and
update the versions and checksums here.
