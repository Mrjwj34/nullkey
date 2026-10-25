import {Composition} from 'remotion';
import {NullkeyHook} from './NullkeyHook';

export const Root = () => (
  <Composition
    id="NullkeyHook"
    component={NullkeyHook}
    durationInFrames={1500}
    fps={30}
    width={1920}
    height={1080}
  />
);
