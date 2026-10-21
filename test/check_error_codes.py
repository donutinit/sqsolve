#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Fails when one SQS- error code carries two different messages.

`make errors` builds docs/manual/errors.md and keeps only the first message of
each code, so a code reused for two meanings stays invisible there. This check
reads the same sources (every .go file except tests) and reports them.

A code with no text after it (or one inside parentheses or a SQL string) is a
reference, not a definition, and is ignored, as are comment lines. Messages are compared after the same
normalisation `make errors` applies (format verbs become "...") and ignoring
case and a final period, so "Library not found." and "library not found" are
one message.

test/errors-multi-message.txt is the baseline of codes that already carried
several wordings when this check was added. It is existing debt, not an
approval: do not add to it, split the code instead.

Usage: check_error_codes.py [--allowlist FILE] [DIR ...]   (default: cmd internal)
"""
import argparse
import pathlib
import re
import sys

# A code followed by a space and text is a definition: "SQS-B033 unpin the backup...".
# A code anywhere else ("(SQS-X999)", 'SQS-J010') is a reference to it.
DEFINITION = re.compile(r'(SQS-[A-Z][0-9]{3}) ([^"`]*)')
VERB = re.compile(r'%[-+ 0-9.]*[a-zA-Z]')


def normalize(text):
    text = VERB.sub('...', text).strip().lower()
    return text.rstrip('.: ')


def collect(roots):
    """Return {code: {normalized message: [first place it appears]}}."""
    found = {}
    for root in roots:
        for path in sorted(pathlib.Path(root).rglob('*.go')):
            if path.name.endswith('_test.go'):
                continue
            for number, line in enumerate(path.read_text(encoding='utf-8').splitlines(), 1):
                if line.lstrip().startswith('//'):
                    continue
                for code, rest in DEFINITION.findall(line):
                    message = normalize(rest)
                    if message:
                        found.setdefault(code, {}).setdefault(message, f'{path}:{number}')
    return found


def read_allowlist(path):
    codes = set()
    if path and pathlib.Path(path).exists():
        for line in pathlib.Path(path).read_text(encoding='utf-8').splitlines():
            line = line.split('#', 1)[0].strip()
            if line:
                codes.add(line.split()[0])
    return codes


def check(roots, allowlist):
    """Return (errors, notes) as lists of text lines."""
    found = collect(roots)
    errors, notes = [], []
    for code in sorted(found):
        messages = found[code]
        if len(messages) > 1 and code not in allowlist:
            errors.append(f'{code} has {len(messages)} different messages:')
            errors.extend(f'    {where}  {message}' for message, where in sorted(messages.items(), key=lambda kv: kv[1]))
    for code in sorted(allowlist):
        if len(found.get(code, {})) <= 1:
            notes.append(f'{code} is in the allowlist but no longer has several messages; remove it')
    return errors, notes


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.split('\n\n')[0])
    parser.add_argument('--allowlist', default=str(pathlib.Path(__file__).with_name('errors-multi-message.txt')))
    parser.add_argument('roots', nargs='*', default=['cmd', 'internal'])
    args = parser.parse_args(argv)
    errors, notes = check(args.roots, read_allowlist(args.allowlist))
    for note in notes:
        print('note:', note, file=sys.stderr)
    if errors:
        print('One error code must mean one thing. Give the second meaning a new code:', file=sys.stderr)
        print('\n'.join(errors), file=sys.stderr)
        return 1
    print('errors-check: every SQS- code has a single message (baseline: %d codes with several wordings)' % len(read_allowlist(args.allowlist)))
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
