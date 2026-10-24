part of 'theme.dart';

/// Zinc neutrals from the Forui preset with the macOS accent blue as primary.
final FColors lightColors = FColors(
  brightness: .light,
  systemOverlayStyle: .dark,
  barrier: Color(0x33000000),
  background: Color(0xFFFFFFFF),
  foreground: Color(0xFF1D1D1F),
  primary: Color(0xFF0A63D6),
  primaryForeground: Color(0xFFFFFFFF),
  secondary: Color(0xFFEDEDF0),
  secondaryForeground: Color(0xFF1D1D1F),
  muted: Color(0xFFF4F4F6),
  mutedForeground: Color(0xFF6E6E73),
  destructive: Color(0xFFD70015),
  destructiveForeground: Color(0xFFFFFFFF),
  error: Color(0xFFD70015),
  errorForeground: Color(0xFFFFFFFF),
  card: Color(0xFFFFFFFF),
  border: Color(0xFFE2E2E6),
  extensions: const [
    AppColors(
      sidebar: Color(0xFFF3F3F5),
      sidebarSelection: Color(0xFFE1E1E6),
      success: Color(0xFF1A7F37),
      warning: Color(0xFF9A5B00),
      terminal: Color(0xFFFBFBFC),
      terminalForeground: Color(0xFF1D1D1F),
      terminalMuted: Color(0xFF86868B),
    ),
  ],
);

final FColors darkColors = FColors(
  brightness: .dark,
  systemOverlayStyle: .light,
  barrier: Color(0x7A000000),
  background: Color(0xFF1E1E20),
  foreground: Color(0xFFF5F5F7),
  primary: Color(0xFF2F7DF0),
  primaryForeground: Color(0xFFFFFFFF),
  secondary: Color(0xFF323236),
  secondaryForeground: Color(0xFFF5F5F7),
  muted: Color(0xFF2A2A2D),
  mutedForeground: Color(0xFFA1A1A6),
  destructive: Color(0xFFE5484D),
  destructiveForeground: Color(0xFFFFFFFF),
  error: Color(0xFFE5484D),
  errorForeground: Color(0xFFFFFFFF),
  card: Color(0xFF252528),
  border: Color(0x24FFFFFF),
  extensions: const [
    AppColors(
      sidebar: Color(0xFF262628),
      sidebarSelection: Color(0xFF3A3A3E),
      success: Color(0xFF3FB950),
      warning: Color(0xFFE3A440),
      terminal: Color(0xFF161618),
      terminalForeground: Color(0xFFE8E8ED),
      terminalMuted: Color(0xFF8E8E93),
    ),
  ],
);

FColors _highContrast(FColors colors) {
  final dark = colors.brightness == Brightness.dark;
  final app = colors.app;
  return colors.copyWith(
    border: dark ? const Color(0x73FFFFFF) : const Color(0xFF8E8E93),
    mutedForeground: dark ? const Color(0xFFD1D1D6) : const Color(0xFF48484D),
    extensions: [
      app.copyWith(
        sidebarSelection: dark
            ? const Color(0xFF55555B)
            : const Color(0xFFC7C7CD),
        terminalMuted: dark ? const Color(0xFFC7C7CC) : const Color(0xFF55555A),
      ),
    ],
  );
}

/// Provides convenient access to theme extensions on [FColors].
extension FColorsExtensions on FColors {
  AppColors get app => extension<AppColors>();
}

/// Color tokens the Forui palette does not cover.
class AppColors extends ThemeExtension<AppColors> {
  /// Sidebar background, slightly darker than the content area.
  final Color sidebar;

  /// Fill of the selected sidebar row.
  final Color sidebarSelection;

  /// Running / healthy state. Always paired with a text label.
  final Color success;

  /// Interrupted / attention state. Always paired with a text label.
  final Color warning;

  final Color terminal;
  final Color terminalForeground;
  final Color terminalMuted;

  const AppColors({
    required this.sidebar,
    required this.sidebarSelection,
    required this.success,
    required this.warning,
    required this.terminal,
    required this.terminalForeground,
    required this.terminalMuted,
  });

  @override
  AppColors copyWith({
    Color? sidebar,
    Color? sidebarSelection,
    Color? success,
    Color? warning,
    Color? terminal,
    Color? terminalForeground,
    Color? terminalMuted,
  }) => AppColors(
    sidebar: sidebar ?? this.sidebar,
    sidebarSelection: sidebarSelection ?? this.sidebarSelection,
    success: success ?? this.success,
    warning: warning ?? this.warning,
    terminal: terminal ?? this.terminal,
    terminalForeground: terminalForeground ?? this.terminalForeground,
    terminalMuted: terminalMuted ?? this.terminalMuted,
  );

  @override
  AppColors lerp(covariant AppColors? other, double t) {
    if (other == null) {
      return this;
    }
    return AppColors(
      sidebar: Color.lerp(sidebar, other.sidebar, t)!,
      sidebarSelection: Color.lerp(
        sidebarSelection,
        other.sidebarSelection,
        t,
      )!,
      success: Color.lerp(success, other.success, t)!,
      warning: Color.lerp(warning, other.warning, t)!,
      terminal: Color.lerp(terminal, other.terminal, t)!,
      terminalForeground: Color.lerp(
        terminalForeground,
        other.terminalForeground,
        t,
      )!,
      terminalMuted: Color.lerp(terminalMuted, other.terminalMuted, t)!,
    );
  }

  @override
  bool operator ==(Object other) =>
      identical(this, other) ||
      other is AppColors &&
          sidebar == other.sidebar &&
          sidebarSelection == other.sidebarSelection &&
          success == other.success &&
          warning == other.warning &&
          terminal == other.terminal &&
          terminalForeground == other.terminalForeground &&
          terminalMuted == other.terminalMuted;

  @override
  int get hashCode => Object.hash(
    sidebar,
    sidebarSelection,
    success,
    warning,
    terminal,
    terminalForeground,
    terminalMuted,
  );
}
