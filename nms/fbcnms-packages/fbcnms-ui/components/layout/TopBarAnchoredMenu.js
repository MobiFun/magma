/**
 * Copyright 2004-present Facebook. All Rights Reserved.
 *
 * This source code is licensed under the BSD-style license found in the
 * LICENSE file in the root directory of this source tree.
 *
 * @flow
 * @format
 */

import React, {useState} from 'react';
import Button from '@material-ui/core/Button';
import Menu from '@material-ui/core/Menu';

type Props = {
  id: string,
  children: any,
  buttonContent: React$Element<any>,
};

export default function TopBarAnchoredMenu(props: Props) {
  const [anchorEl, setAnchorEl] = useState<?HTMLElement>(null);
  return (
    <>
      <Button
        aria-owns={anchorEl ? props.id : null}
        aria-haspopup="true"
        onClick={e => setAnchorEl(e.currentTarget)}
        color="inherit">
        {props.buttonContent}
      </Button>
      <Menu
        id={props.id}
        anchorEl={anchorEl}
        anchorOrigin={{vertical: 'top', horizontal: 'right'}}
        transformOrigin={{vertical: 'top', horizontal: 'right'}}
        open={!!anchorEl}
        onClose={() => setAnchorEl(null)}>
        {props.children}
      </Menu>
    </>
  );
}
