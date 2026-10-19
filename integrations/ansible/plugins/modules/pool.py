#!/usr/bin/python
# -*- coding: utf-8 -*-
# Copyright: (c) 2026, Onur Baydas
# GNU General Public License v3.0+ (see LICENSE or https://www.gnu.org/licenses/gpl-3.0.txt)

from __future__ import absolute_import, division, print_function

__metaclass__ = type

try:
    from ansible_collections.onurbaydas.virtomir.plugins.modules.virtomir_pool import (
        DOCUMENTATION, EXAMPLES, RETURN, main
    )
except ImportError:
    try:
        from .virtomir_pool import DOCUMENTATION, EXAMPLES, RETURN, main
    except (ImportError, ValueError):
        from virtomir_pool import DOCUMENTATION, EXAMPLES, RETURN, main

if __name__ == '__main__':
    main()
