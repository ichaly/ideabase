DROP TABLE IF EXISTS "public"."sys_area";
CREATE TABLE "public"."sys_area" (
  id bigserial constraint area_pk primary key,
  name varchar(255) COLLATE "pg_catalog"."default",
  weight int4
)
;
CREATE TABLE "public"."sys_edge" (
  "user_id" int8 NOT NULL,
  "team_id" int8 NOT NULL
)
;

-- ----------------------------
-- Table structure for sys_item
-- ----------------------------
CREATE TABLE "public"."sys_item" (
  id bigserial constraint item_pk primary key,
  user_id int8
)
;

-- ----------------------------
-- Table structure for sys_team
-- ----------------------------
DROP TABLE IF EXISTS "public"."sys_team";
CREATE TABLE "public"."sys_team" (
  id bigserial constraint team_pk primary key,
  pid int8,
  area_id int8
)
;

-- ----------------------------
-- Table structure for sys_user
-- ----------------------------
CREATE TABLE "public"."sys_user" (
  id bigserial constraint user_pk primary key,
  name text COLLATE "pg_catalog"."default"
)
;

-- ----------------------------
-- Foreign Keys structure for table sys_edge
-- ----------------------------
ALTER TABLE "public"."sys_edge" ADD CONSTRAINT "edge_team_id_fk" FOREIGN KEY ("team_id") REFERENCES "public"."sys_team" ("id") ON DELETE NO ACTION ON UPDATE NO ACTION;
ALTER TABLE "public"."sys_edge" ADD CONSTRAINT "edge_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."sys_user" ("id") ON DELETE NO ACTION ON UPDATE NO ACTION;

-- ----------------------------
-- Foreign Keys structure for table sys_item
-- ----------------------------
ALTER TABLE "public"."sys_item" ADD CONSTRAINT "item_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."sys_user" ("id") ON DELETE NO ACTION ON UPDATE NO ACTION;

-- ----------------------------
-- Foreign Keys structure for table sys_team
-- ----------------------------
ALTER TABLE "public"."sys_team" ADD CONSTRAINT "team_area_id_fk" FOREIGN KEY ("area_id") REFERENCES "public"."sys_area" ("id") ON DELETE NO ACTION ON UPDATE NO ACTION;
ALTER TABLE "public"."sys_team" ADD CONSTRAINT "team_team_id_fk" FOREIGN KEY ("pid") REFERENCES "public"."sys_team" ("id") ON DELETE NO ACTION ON UPDATE NO ACTION;
