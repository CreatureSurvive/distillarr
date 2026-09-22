# Vendored shaders

These mpv-format shaders are loaded by libplacebo at run time (`custom_shader_path`).
They are separate data files, each unmodified apart from the one `#define` some presets
rewrite (see `preset.go`), and each keeps its own copyright header.

| File(s) | Origin | Licence |
|---|---|---|
| `Anime4K_*.glsl` | https://github.com/bloc97/Anime4K | MIT (`LICENSE-Anime4K`) |
| `FSR.glsl` | AMD FidelityFX FSR 1.0.2, mpv port by agyild: https://gist.github.com/agyild/82219c545228d70c5604f865ce0b0ce5 | MIT (`LICENSE-FSR`) |
| `NVScaler.glsl` | NVIDIA Image Scaling 1.0.2, mpv port by agyild: https://gist.github.com/agyild/7e8951915b2bf24526a9343d951db214 | MIT (`LICENSE-NIS`) |
| `ravu-lite-r3.glsl` | RAVU-Lite r3 (bjin/mpv-prescalers `ravu-lite-r3.hook`): https://github.com/bjin/mpv-prescalers | LGPL-3.0 (`LICENSE-LGPL-3.0`) |
| `FSRCNNX_x2_*.glsl` | igv/FSRCNN-TensorFlow release 1.1: https://github.com/igv/FSRCNN-TensorFlow | LGPL-3.0 (`LICENSE-LGPL-3.0`) |
| `SSimSuperRes.glsl` | Shiandow / igv: https://gist.github.com/igv/2364ffa6e81540f29cb7ab4c9bc05b6b | LGPL-3.0 (`LICENSE-LGPL-3.0`) |

The LGPL files are embedded in the binary as data, not linked as code; replacing one means
replacing the file here and rebuilding.
