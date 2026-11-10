// Fragment partage : QR code deterministe en Typst pur (contrat `qr/v1`, README § QR codes).
// Aucun paquet, aucun plugin, aucune image : du vectoriel. Couleurs : la couleur la plus
// sombre de `brand.colors` sur fond blanc (le blanc est la zone de silence et le fond).
//
// Le fragment ne calcule pas le QR : il lit la matrice de modules que la fonction de gabarit
// `qrModules` (moteur, voir README) rend en texte, et la dessine. Usage dans un gabarit :
//   #qr-code([ appel de qrModules sur le texte, niveau M ], size: 25mm)  (voir le README pour la syntaxe exacte)
// La matrice est donnee sous trois formes equivalentes : une chaine, ou un contenu `[...]`
// qui la porte (cas d'un gabarit : l'action passe par reference, jamais dans le code), ou
// un tableau de chaines (une par rangee).
// Toute entree mal formee echoue par une erreur de compilation dont le message commence par `qr/v1:`.
#import "/brand.typ": brand

// Bornes du contrat (README § QR codes). Version 25 = 117 modules : de quoi tenir 512 octets au niveau H.
#let limits = (
  min-modules: 21, // version 1
  max-modules: 117, // version 25
  quiet: 4, // zone de silence, en modules
  min-contrast: 4.5, // contraste minimal de la couleur sombre contre le blanc
)

#let fail(msg) = panic("qr/v1: " + msg)
#let ensure(ok, msg) = if not ok { fail(msg) }

#let plain(x) = {
  if type(x) == str { x } else if type(x) == content {
    if x.has("text") { x.text } else if x.has("children") { x.children.map(plain).join() } else {
      fail("expected the matrix text of qrModules (the qrModules function of the template, inside [ ])")
    }
  } else { fail("expected a string, its content or an array of row strings, got " + str(type(x))) }
}

// Matrice : tableau de rangees (chaines de `0` et `1`), carree, de cote 21 + 4k (versions 1 a 25).
#let parse-matrix(x) = {
  let rows = if type(x) == array { x } else { plain(x).split("/") }
  let n = rows.len()
  ensure(n >= limits.min-modules and n <= limits.max-modules and calc.rem(n - limits.min-modules, 4) == 0,
    "the matrix must have 21 + 4k rows, at most " + str(limits.max-modules) + ", got " + str(n))
  for row in rows {
    ensure(type(row) == str and row.match(regex("^[01]+$")) != none, "a row may only contain the characters 0 and 1")
    ensure(row.len() == n, "the matrix must be square: " + str(n) + " rows of " + str(n) + " modules")
  }
  rows
}

// Luminance relative (WCAG) d'une couleur de la charte, calculee en RGB lineaire.
#let luminance(col) = {
  // les composantes sont des ratios : ratio / 100% donne un nombre
  let (r, g, b) = col.linear-rgb().components(alpha: false).map(v => v / 100%)
  0.2126 * r + 0.7152 * g + 0.0722 * b
}

// La couleur de la charte la plus sombre, donc la plus contrastee contre le blanc : un lecteur de QR
// veut des modules sombres sur fond clair. Refus si meme celle-la ne contraste pas assez.
#let dark-color() = {
  let cols = brand.colors.values().filter(v => type(v) == color)
  ensure(cols.len() > 0, "brand.colors holds no colour")
  let best = cols.fold(cols.first(), (a, b) => if luminance(b) < luminance(a) { b } else { a })
  ensure(1.05 / (luminance(best) + 0.05) >= limits.min-contrast, "no brand colour is dark enough to be read against white")
  best
}

// `size` est le cote total du carre, zone de silence comprise (4 modules de chaque cote).
#let qr-code(data, size: 30mm) = {
  ensure(type(size) == length and size > 0pt, "size must be a positive length")
  let rows = parse-matrix(data)
  let n = rows.len()
  let u = size / (n + 2 * limits.quiet)
  let dark = dark-color()
  box(width: size, height: size, fill: white, {
    for (y, row) in rows.enumerate() {
      let cs = row.codepoints()
      let x = 0
      // un rectangle par suite horizontale de modules sombres
      while x < n {
        if cs.at(x) == "1" {
          let x0 = x
          while x < n and cs.at(x) == "1" { x += 1 }
          place(top + left, dx: (x0 + limits.quiet) * u, dy: (y + limits.quiet) * u,
            rect(width: (x - x0) * u, height: u, fill: dark, stroke: none, inset: 0pt, outset: 0pt, radius: 0pt))
        } else { x += 1 }
      }
    }
  })
}
