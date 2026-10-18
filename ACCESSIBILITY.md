# Accessibility Statement for VirtoMir

VirtoMir is committed to ensuring digital accessibility for all users, including operators and contributors with disabilities. We continually improve the user experience for everyone and apply relevant accessibility standards.

## Measures to Support Accessibility

VirtoMir takes the following measures to ensure accessibility across its web control deck:

- **Keyboard Navigation**: All interactive elements (navigation items, machine cards, dialogs, form inputs, action menus) are accessible and operable using standard keyboard navigation (`Tab`, `Shift+Tab`, `Enter`, `Space`, `Escape`).
- **Semantic HTML & ARIA Attributes**: Svelte components utilize semantic HTML elements and proper ARIA attributes (`role`, `aria-label`, `aria-expanded`, `aria-live`) to support screen readers and assistive technologies.
- **Contrast & Typography**: Design tokens defined in `web/src/app.css` maintain contrast ratios complying with WCAG 2.1 Level AA standards across both light and dark modes.
- **Motion Reduction**: Respects user preferences for reduced motion (`prefers-reduced-motion`) across animations and transitions.

## Feedback and Reporting Barriers

We welcome your feedback on the accessibility of VirtoMir. If you encounter accessibility barriers:

- **Open an Issue**: Submit an issue on GitHub with the `accessibility` label: [New Accessibility Issue](https://github.com/onurbaydas/VirtoMir/issues/new/choose).
- **Contact**: Reach out via GitHub Discussions or email `onurbaydas1@gmail.com`.

Please provide the page or dialog where you encountered the barrier, assistive technology used, and the expected behavior.
