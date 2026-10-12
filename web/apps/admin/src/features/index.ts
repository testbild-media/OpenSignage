import type { Feature } from './types';
import { systemFeature } from './system';
import { mediaFeature } from './media';
import { presentationsFeature } from './presentations';
import { playlistsFeature } from './playlists';
import { productsFeature } from './products';
import { zonesFeature } from './zones';
import { schedulesFeature } from './schedules';
import { devicesFeature } from './devices';

export const features: Feature[] = [
  systemFeature,
  mediaFeature,
  presentationsFeature,
  playlistsFeature,
  productsFeature,
  zonesFeature,
  schedulesFeature,
  devicesFeature,
];
