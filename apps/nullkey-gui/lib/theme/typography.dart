part of 'theme.dart';

// The interaction contract asks for the system font. On macOS these aliases
// resolve to SF Pro Text / SF Pro Display; CoreText falls back to PingFang SC
// for Chinese.
const String _textFamily = 'CupertinoSystemText';
const String _displayFamily = 'CupertinoSystemDisplay';
const List<String> _cjkFallback = ['PingFang SC'];

/// Monospaced family for terminal output, paths, hosts and ports.
const String monoFontFamily = 'Menlo';
const List<String> monoFontFallback = ['PingFang SC'];

FTypography _typography({required FColors colors, required bool touch}) =>
    FTypography(
      display: _typeface(colors: colors, display: true, touch: touch),
      body: _typeface(colors: colors, display: false, touch: touch),
    );

/// Desktop sizes follow the macOS scale (13pt body, 11–12pt secondary).
/// Touch sizes stay one step larger.
FTypeface _typeface({
  required FColors colors,
  required bool display,
  required bool touch,
}) {
  final step = touch ? 2.0 : 0.0;
  TextStyle style(double size, double height) {
    final fontSize = size + step;
    // SF Pro Display is tuned for 20pt and above; smaller headings use Text.
    final family = display && fontSize >= 20 ? _displayFamily : _textFamily;
    return TextStyle(
      color: colors.foreground,
      fontFamily: family,
      fontFamilyFallback: _cjkFallback,
      fontSize: fontSize,
      height: height,
      leadingDistribution: .even,
    );
  }

  return FTypeface(
    fontFamily: display ? _displayFamily : _textFamily,
    fontFamilyFallback: _cjkFallback,
    xs3: style(9, 1.2),
    xs2: style(10, 1.3),
    xs: style(12, 1.35),
    sm: style(13, 1.4),
    md: style(14, 1.45),
    lg: style(16, 1.4),
    xl: style(20, 1.3),
    xl2: style(24, 1.25),
    xl3: style(28, 1.2),
    xl4: style(34, 1.15),
    xl5: style(48, 1.1),
    xl6: style(60, 1),
    xl7: style(72, 1),
    xl8: style(96, 1),
  );
}
