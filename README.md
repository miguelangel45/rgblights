# RGB Lights

Refleja los colores de una región de tu pantalla en las luces RGB de tu PC
(placa ASUS Aura, GPU, RAM, ventiladores, periféricos…) usando el servidor SDK
de [OpenRGB](https://openrgb.org).

Vive en la bandeja del sistema, junto al reloj de Windows.

## Requisitos

1. **OpenRGB** instalado y abierto, con el servidor SDK encendido:
   pestaña *SDK Server* → *Start Server* (puerto 6742). Para que arranque solo,
   en *Settings* activa *Start at login*, *Start minimized* y *Start server*.
2. **Cierra Armoury Crate / Aura** o detén su servicio (`LightingService`).
   Si los dos programas controlan las luces a la vez, se pelean.
3. Comprueba en OpenRGB que aparecen tus dispositivos ASUS y que puedes
   cambiarles el color a mano.

### Periféricos Corsair (sin cerrar iCUE)

Los dispositivos Corsair se controlan con el **SDK de iCUE**, con iCUE abierto:

1. En iCUE: *Configuración* → activa el **SDK** (*Habilitar SDK*).
2. RGB Lights necesita la librería oficial `iCUESDK.x64_2019.dll` (SDK v4). La
   busca junto a `rgblights.exe` y en las carpetas de programas que la incluyen
   (por ejemplo Stream Deck). Si no la tienes, descárgala de los *releases* de
   `CorsairOfficial/cue-sdk` en GitHub y ponla junto al `.exe`.
3. Mientras la app refleja la pantalla, toma el control exclusivo de la
   iluminación. Al **pausar** o cerrar la app, iCUE recupera sus efectos.
4. Con *Degradado por LED*, cada LED Corsair toma el color de su posición
   física (un teclado muestra una “miniatura” de la región).

Para desactivarlo: `"disable_corsair": true` en la configuración.

### Teclados QMK/Vial (VialRGB)

Los teclados con firmware **Vial** y RGB Matrix (por ejemplo un Corne) se
detectan solos por USB: no hace falta OpenRGB ni otro programa.

- Sin *Degradado por LED*, todo el teclado toma **un solo color** (efecto
  *Solid Color*). En teclados partidos así se encienden **las dos mitades**.
- Con *Degradado por LED*, usa el efecto *Gradient Left Right*: el **tono** va
  del de la izquierda de la región al de la derecha, en las dos mitades. El
  brillo y la saturación son los mismos en todo el teclado. Si el camino entre
  los dos tonos es "hacia atrás" en la rueda de color (por ejemplo, de amarillo
  a rojo), el firmware solo podría dar la vuelta completa pasando por un arcoíris,
  así que en ese caso se usa un color intermedio plano.
- Menú → *Teclado Vial: color por tecla*: con *Degradado por LED*, cada tecla
  toma el color de su posición (efecto *Direct*). En teclados partidos como el
  Corne, el firmware solo muestra esos colores en la mitad conectada por USB.
- Al pausar o cerrar la app, el teclado vuelve al efecto que tenía.
- El firmware limita el brillo máximo (en un Corne suele ser bajo), así que los
  colores se ven más tenues que en otros dispositivos.
- Diagnóstico: `go run ./cmd/vialprobe` (sin cambiar nada).
- Para desactivarlo: `"disable_keyboards": true`.

## Compilar

```bash
go build -ldflags "-H=windowsgui -s -w" -o rgblights.exe .
```

## Uso

La primera vez que abres `rgblights.exe` se oscurece la pantalla: arrastra un
rectángulo sobre la zona que quieres seguir. Esc o clic derecho cancela.
Si no eliges una región, se usa la pantalla principal completa.

Menú del ícono de la bandeja:

| Opción | Qué hace |
|---|---|
| Seleccionar región… | Vuelve a elegir la zona (funciona en cualquier monitor) |
| Usar pantalla principal completa | Olvida la región elegida |
| Región por dispositivo | Cada dispositivo (placa/ventiladores, ratón…) puede seguir su propia zona: *Elegir región…* o *Usar la región general* |
| Pausar | Deja de actualizar las luces |
| Colores vivos | Da más peso a los píxeles con color que a los grises/negros |
| Degradado por LED | Reparte la región de izquierda a derecha entre los LEDs de cada dispositivo, en lugar de un único color |
| Brillo / Suavizado | Intensidad y velocidad de las transiciones |
| Dispositivos | Lo que se está controlando |
| Reconectar a OpenRGB | Vuelve a detectar dispositivos |
| Iniciar con Windows | Arranca automáticamente al iniciar sesión |

**Clic izquierdo en el ícono** (o *Ver colores y dispositivos…*) abre una ventana
en vivo con el color de la región, los dispositivos que ve OpenRGB y el color
que se envía a cada LED.

### ¿Las luces no cambian?

- Menú → **Probar luces**: pone todo en rojo, verde, azul y blanco (2 s cada uno).
- Si una zona *Addressable RGB Header* (donde van ventiladores o tiras ARGB) tiene
  **0 LEDs**, RGB Lights le pone 60 automáticamente. Para fijar otro número, usa
  `"zone_sizes": {"Addressable RGB Header 1": 24}` en la configuración.
- **iCUE (Corsair)** y Armoury Crate pueden pelear con OpenRGB por las mismas
  luces. Ciérralos, y en OpenRGB usa *Settings → Rescan Devices* para que
  aparezcan también tus periféricos Corsair.
- La GPU y la RAM solo aparecen si OpenRGB se ejecuta **como administrador**.
- Diagnóstico desde la terminal:

  ```bash
  go run ./cmd/rgbinfo          # lista dispositivos, zonas y LEDs
  go run ./cmd/rgbinfo -flash   # rojo/verde/azul/blanco en todo (cierra antes RGB Lights)
  ```

## Configuración

`%APPDATA%\rgblights\config.json` (menú → *Abrir archivo de configuración*,
luego *Recargar configuración*):

```json
{
  "server": "127.0.0.1:6742",
  "region": { "x": 0, "y": 0, "w": 1920, "h": 1080 },
  "fps": 30,
  "smoothing_ms": 120,
  "brightness": 1,
  "saturation": 1.3,
  "vivid": true,
  "spread": false,
  "devices": ["aura", "rtx"],
  "paused": false
}
```

- `devices`: controla solo los dispositivos cuyo nombre contenga alguno de estos
  textos (sin importar mayúsculas). Si lo dejas vacío, controla todos.
- `saturation`: 1 deja el color tal cual; 1.3–1.6 suele verse mejor en LEDs.

El registro queda en `%APPDATA%\rgblights\rgblights.log`.

## Limitaciones

- La captura usa GDI. Los juegos en **pantalla completa exclusiva** pueden
  salir negros; usa el modo *ventana sin bordes*.
- Mientras se muestra UAC o la pantalla de bloqueo, las luces conservan el último color.

## Licencia

[MIT](LICENSE).
